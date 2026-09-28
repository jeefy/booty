package server

import (
	"bytes"
	"debug/pe"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

var (
	testKernel = append(append(bytes.Repeat([]byte{0}, 0x202), "HdrS"...), bytes.Repeat([]byte{'K'}, 300)...)
	testInitrd = append([]byte{0x28, 0xb5, 0x2f, 0xfd}, bytes.Repeat([]byte{'I'}, 500)...)
	testUcode  = []byte("070701UCODE")
)

func biosUKI(ucode bool) map[string][]byte {
	s := map[string][]byte{".linux": testKernel, ".initrd": testInitrd, ".cmdline": padded(sampleUKICmdline), ".osrel": []byte("ID=bluefin\n")}
	if ucode {
		s[".ucode"] = testUcode
	}
	return s
}

func TestPayloadSize(t *testing.T) {
	for _, tc := range []struct{ virt, raw, want uint32 }{
		{549, 1024, 549}, {1024, 1024, 1024}, {0, 512, 512}, {2048, 1024, 1024},
	} {
		s := &pe.Section{SectionHeader: pe.SectionHeader{VirtualSize: tc.virt, Size: tc.raw}}
		if got := payloadSize(s); got != int64(tc.want) {
			t.Errorf("virt %d raw %d: %d want %d", tc.virt, tc.raw, got, tc.want)
		}
	}
}

func TestBluefinBIOSRender(t *testing.T) {
	srv, dir := newTestServer(t)
	installBluefinFixture(t, dir)
	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin"}`)
	dirURL := "http://192.168.1.10:8080/bluefin/" + bluefinDashMAC
	sec := dirURL + "/bluefin-server-netboot_" + bluefinTestVersion
	rewritten := strings.Replace(sampleUKICmdline, "blockdev,bootorigin:rootdisk:", "blockdev:rootdisk:"+dirURL+"/", 1)

	for _, ucode := range []bool{false, true} {
		writeUKI(t, dir, bluefinTestVersion, biosUKI(ucode))
		touchLater(t, dir)
		r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC, "")
		initrds := "initrd " + sec + ".initrd || goto bios-failed\n"
		if ucode {
			initrds = "initrd " + sec + ".ucode || goto bios-failed\n" + initrds
		}
		for _, want := range []string{
			"#!ipxe\niseq ${platform} pcbios && goto bios ||\n",
			"\nchain " + sec + ".efi " + rewritten + " || goto chain-failed\n",
			":bios\nset menu-timeout 5000\n",
			"choose --timeout ${menu-timeout} --default bios-netboot selected || goto bios-disk\n",
			":bios-netboot\ncpuid --ext 29 || goto bios-not64\nimgfree\nkernel " + sec + ".linux " + rewritten + " || goto bios-failed\n" + initrds + "boot || goto bios-failed\n",
		} {
			if !strings.Contains(r.body, want) {
				t.Errorf("ucode=%v: missing %q:\n%s", ucode, want, r.body)
			}
		}
		if !ucode && strings.Contains(r.body, ".ucode") {
			t.Errorf("no .ucode section, no ucode initrd:\n%s", r.body)
		}
	}
	if h, _ := hardware.Get(bluefinMAC); h.Booted != "" {
		t.Fatalf("the render records nothing: %+v", h)
	}

	t.Run("Secure Boot blocks the EFI chain, not BIOS", func(t *testing.T) {
		r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC+"&sb=1", "")
		if strings.Contains(r.body, "\nchain ") || !strings.Contains(r.body, "switch to UEFI HTTP Boot") || !strings.Contains(r.body, "\nkernel "+sec+".linux ") {
			t.Fatalf("%s", r.body)
		}
		if h, _ := hardware.Get(bluefinMAC); !h.SecureBoot {
			t.Fatal("flag recorded")
		}
		r = do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC+"&preview=1", "")
		if strings.Contains(r.body, "\nchain ") || !strings.Contains(r.body, "\nkernel "+sec+".linux ") {
			t.Fatalf("a secureBoot-flagged host still gets the BIOS branch:\n%s", r.body)
		}
		do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC, "")
	})

	t.Run("pending install on BIOS", func(t *testing.T) {
		register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","doInstall":true,"installDisk":"/dev/sda"}`)
		r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC, "")
		if !strings.Contains(r.body, "echo Booty: an install of Bluefin Server to /dev/sda is pending for srv1, but installing\n") || !strings.Contains(r.body, "--default bios-netboot selected") {
			t.Fatalf("%s", r.body)
		}
		register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","mode":"installed","doInstall":true,"installDisk":"/dev/sda"}`)
		if r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC, ""); !strings.Contains(r.body, "--default bios-disk selected") || !strings.Contains(r.body, "\nchain ") {
			t.Fatalf("an installed host being reinstalled: EFI chainloads, BIOS defaults to disk:\n%s", r.body)
		}
		register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","mode":"installed"}`)
		if r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC, ""); strings.Contains(r.body, "bios") || !strings.HasSuffix(r.body, "\nexit\n") {
			t.Fatalf("an installed host exits on every platform:\n%s", r.body)
		}
	})

	t.Run("fail closed on BIOS too", func(t *testing.T) {
		register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin"}`)
		for name, sections := range map[string]map[string][]byte{
			"no bootorigin": {".linux": testKernel, ".initrd": testInitrd, ".cmdline": padded("usrhash=abc")},
			"no .linux":     {".initrd": testInitrd, ".cmdline": padded(sampleUKICmdline)},
			"no .initrd":    {".linux": testKernel, ".cmdline": padded(sampleUKICmdline)},
		} {
			writeUKI(t, dir, bluefinTestVersion, sections)
			touchLater(t, dir)
			r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC, "")
			if strings.Contains(r.body, "\nkernel ") || !strings.Contains(r.body, "cannot boot it in BIOS mode") || !strings.Contains(r.body, "--default bios-disk selected") {
				t.Fatalf("%s:\n%s", name, r.body)
			}
		}
	})
}

var ukiTouches int

// touchLater moves the UKI's mtime so the parsed-UKI cache sees a new file.
func touchLater(t *testing.T, dir string) {
	t.Helper()
	ukiTouches++
	path := filepath.Join(dir, "bluefin", bluefinTestVersion, "bluefin-server-netboot_"+bluefinTestVersion+".efi")
	later := time.Now().Add(time.Duration(ukiTouches) * time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
}

func TestBluefinSectionRoutes(t *testing.T) {
	srv, dir := newTestServer(t)
	writeBluefinRelease(t, dir, bluefinPrevVersion, "previous")
	installBluefinFixture(t, dir)
	writeUKI(t, dir, bluefinTestVersion, biosUKI(true))
	writeUKI(t, dir, bluefinPrevVersion, biosUKI(false))
	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:c1","hostname":"fc","os":"flatcar"}`)
	base := srv.URL + "/bluefin/" + bluefinDashMAC + "/bluefin-server-netboot_"

	get := func(t *testing.T, method, url string, hdr map[string]string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, url, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}
	body := func(t *testing.T, resp *http.Response) []byte {
		t.Helper()
		var b bytes.Buffer
		if _, err := b.ReadFrom(resp.Body); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}

	for name, want := range map[string][]byte{
		bluefinTestVersion + ".linux":  testKernel,
		bluefinTestVersion + ".initrd": testInitrd,
		bluefinTestVersion + ".ucode":  testUcode,
		bluefinPrevVersion + ".linux":  testKernel,
	} {
		resp := get(t, http.MethodGet, base+name+"?preview=1", nil)
		if got := body(t, resp); resp.StatusCode != 200 || !bytes.Equal(got, want) || resp.Header.Get("Content-Type") != "application/octet-stream" || resp.Header.Get("ETag") == "" || resp.Header.Get("Last-Modified") == "" {
			t.Errorf("%s: %d %q %v", name, resp.StatusCode, got, resp.Header)
		}
		head := get(t, http.MethodHead, base+name+"?preview=1", nil)
		if head.StatusCode != 200 || head.ContentLength != int64(len(want)) {
			t.Errorf("%s: HEAD %d length %d", name, head.StatusCode, head.ContentLength)
		}
	}
	if !bytes.Equal(testKernel[0x202:0x206], []byte("HdrS")) {
		t.Fatal("fixture")
	}

	t.Run("Range and conditional requests", func(t *testing.T) {
		resp := get(t, http.MethodGet, base+bluefinTestVersion+".linux?preview=1", map[string]string{"Range": "bytes=514-517"})
		if got := body(t, resp); resp.StatusCode != http.StatusPartialContent || string(got) != "HdrS" {
			t.Fatalf("%d %q", resp.StatusCode, got)
		}
		etag := get(t, http.MethodHead, base+bluefinTestVersion+".initrd?preview=1", nil).Header.Get("ETag")
		if resp := get(t, http.MethodGet, base+bluefinTestVersion+".initrd?preview=1", map[string]string{"If-None-Match": etag}); resp.StatusCode != http.StatusNotModified {
			t.Fatalf("If-None-Match: %d", resp.StatusCode)
		}
		if etag == get(t, http.MethodHead, base+bluefinTestVersion+".linux?preview=1", nil).Header.Get("ETag") {
			t.Fatal("each section has its own ETag")
		}
	})

	for name, path := range map[string]string{
		"missing section":  base + bluefinPrevVersion + ".ucode",
		"unknown version":  base + "20250101.1.linux",
		"unknown section":  base + bluefinTestVersion + ".cmdline",
		"flatcar host":     srv.URL + "/bluefin/aa-bb-cc-dd-ee-c1/bluefin-server-netboot_" + bluefinTestVersion + ".linux",
		"flatcar initrd":   srv.URL + "/bluefin/aa-bb-cc-dd-ee-c1/bluefin-server-netboot_" + bluefinTestVersion + ".initrd",
		"unknown host":     srv.URL + "/bluefin/aa-bb-cc-dd-ee-99/bluefin-server-netboot_" + bluefinTestVersion + ".linux",
		"unversioned name": srv.URL + "/bluefin/" + bluefinDashMAC + "/bluefin-server-netboot.linux",
	} {
		if resp := get(t, http.MethodGet, path, nil); resp.StatusCode != 404 {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
	if _, ok := hardware.Get("aa:bb:cc:dd:ee:99"); ok {
		t.Fatal("never registers without --autoRegister=bluefin")
	}

	t.Run("bookkeeping", func(t *testing.T) {
		get(t, http.MethodGet, base+bluefinTestVersion+".initrd", nil)
		get(t, http.MethodGet, base+bluefinTestVersion+".ucode", nil)
		get(t, http.MethodHead, base+bluefinTestVersion+".linux", nil)
		if h, _ := hardware.Get(bluefinMAC); h.Booted != "" || h.NetbootPlatform != "" {
			t.Fatalf("initrds and HEAD record nothing: %+v", h)
		}
		get(t, http.MethodGet, base+bluefinTestVersion+".linux", nil)
		h, _ := hardware.Get(bluefinMAC)
		if h.Booted == "" || h.IP != "127.0.0.1" || h.NetbootPlatform != hardware.PlatformPCBIOS {
			t.Fatalf("the kernel GET records the boot: %+v", h)
		}
		get(t, http.MethodGet, base+bluefinTestVersion+".efi", nil)
		if h, _ := hardware.Get(bluefinMAC); h.NetbootPlatform != hardware.PlatformEFI {
			t.Fatalf("the UKI GET records efi: %+v", h)
		}
		register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","mode":"installed"}`)
		if resp := get(t, http.MethodGet, base+bluefinTestVersion+".linux", nil); resp.StatusCode != 404 {
			t.Fatalf("an installed host gets no kernel: %d", resp.StatusCode)
		}
	})
}

func TestBluefinBIOSNeverInstalls(t *testing.T) {
	for _, mode := range []string{config.ClearOnIgnition, config.ClearOnNextBoot} {
		t.Run(mode, func(t *testing.T) {
			srv, dir := newTestServer(t)
			installBluefinFixture(t, dir)
			writeUKI(t, dir, bluefinTestVersion, biosUKI(false))
			viper.Set(config.DoInstallClearOn, mode)
			register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","doInstall":true,"installDisk":"/dev/sda"}`)
			kernel := srv.URL + "/bluefin/" + bluefinDashMAC + "/bluefin-server-netboot_" + bluefinTestVersion + ".linux"

			if r := do(t, http.MethodGet, kernel, ""); r.status != 200 {
				t.Fatalf("%+v", r)
			}
			h, _ := hardware.Get(bluefinMAC)
			if h.InstallServedAt != "" || !h.DoInstall {
				t.Fatalf("a BIOS boot stamps no install: %+v", h)
			}
			_, files, units := bluefinNode(t, srv.URL, bluefinMAC, "")
			if _, ok := units["booty-install.service"]; ok {
				t.Fatal("the node config after a BIOS netboot carries no install unit")
			}
			if files["/etc/hostname"].contents != "srv1\n" {
				t.Fatal("the rest of the node config is unchanged")
			}
			if h, _ := hardware.Get(bluefinMAC); !h.DoInstall || h.Installed() {
				t.Fatalf("doInstall survives the BIOS boot for the next UEFI one: %+v", h)
			}

			do(t, http.MethodGet, srv.URL+"/bluefin/"+bluefinDashMAC+"/bluefin-server-netboot.efi", "")
			if _, _, units := bluefinNode(t, srv.URL, bluefinMAC, "?preview=1"); units["booty-install.service"].Name == "" {
				t.Fatal("the next EFI netboot installs again")
			}
		})
	}
}
