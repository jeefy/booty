package server

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
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

// synthPE is a minimal PE32+ image as debug/pe reads it: DOS stub, PE
// signature, COFF header without optional header, and one section per
// entry holding its bytes.
func synthPE(t *testing.T, sections map[string][]byte) []byte {
	t.Helper()
	names := make([]string, 0, len(sections))
	for n := range sections {
		names = append(names, n)
	}
	const dosSize, peOff = 0x40, 0x40
	headers := peOff + 4 + binary.Size(pe.FileHeader{}) + len(names)*binary.Size(pe.SectionHeader32{})
	var buf bytes.Buffer
	dos := make([]byte, dosSize)
	copy(dos, "MZ")
	binary.LittleEndian.PutUint32(dos[0x3c:], peOff)
	buf.Write(dos)
	buf.WriteString("PE\x00\x00")
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(binary.Write(&buf, binary.LittleEndian, pe.FileHeader{Machine: pe.IMAGE_FILE_MACHINE_AMD64, NumberOfSections: uint16(len(names))}))
	offset := uint32(headers)
	var data []byte
	for _, n := range names {
		var h pe.SectionHeader32
		copy(h.Name[:], n)
		h.VirtualSize = uint32(len(sections[n]))
		h.SizeOfRawData = uint32(len(sections[n]))
		h.PointerToRawData = offset
		must(binary.Write(&buf, binary.LittleEndian, h))
		offset += uint32(len(sections[n]))
		data = append(data, sections[n]...)
	}
	buf.Write(data)
	return buf.Bytes()
}

// Shaped like projectbluefin/server's bluefin-server-netboot_2026.09.0.efi
// .cmdline, for bluefinTestVersion.
const sampleUKICmdline = "usrhash=2489f248fef58abd39431809da77c209765951b398222ed1f575c5816553e89d root=tmpfs rd.systemd.pull=raw,machine,verify=signature,blockdev,bootorigin:rootdisk:bluefin-server_" + bluefinTestVersion + ".raw systemd.verity_usr_data=/dev/disk/by-loop-ref/rootdisk.raw-part1 systemd.verity_usr_hash=/dev/disk/by-loop-ref/rootdisk.raw-part2 mount.usr=/dev/mapper/usr mount.usrfstype=erofs mount.usrflags=ro lockdown=integrity console=tty0 console=ttyS0,115200"

func writeUKI(t *testing.T, dir, version string, sections map[string][]byte) {
	t.Helper()
	path := filepath.Join(dir, "bluefin", version, "bluefin-server-netboot_"+version+".efi")
	if err := os.WriteFile(path, synthPE(t, sections), 0o644); err != nil {
		t.Fatal(err)
	}
}

func padded(s string) []byte { return append([]byte(s+"\n"), make([]byte, 300)...) }

func TestUKICmdline(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	got, err := ukiCmdline(write("uki.efi", synthPE(t, map[string][]byte{".linux": []byte("KERNEL"), ".cmdline": padded(sampleUKICmdline), ".osrel": []byte("ID=bluefin")})))
	if err != nil || got != sampleUKICmdline {
		t.Fatalf("got %q, %v", got, err)
	}
	for name, data := range map[string][]byte{
		"no-cmdline.efi": synthPE(t, map[string][]byte{".linux": []byte("KERNEL")}),
		"empty.efi":      synthPE(t, map[string][]byte{".cmdline": make([]byte, 64)}),
		"not-pe.efi":     []byte("UKI-" + bluefinTestVersion),
	} {
		if _, err := ukiCmdline(write(name, data)); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if real := os.Getenv("BOOTY_BLUEFIN_UKI"); real != "" {
		got, err := ukiCmdline(real)
		if err != nil || !strings.HasPrefix(got, "usrhash=") || !strings.Contains(got, ",bootorigin:rootdisk:bluefin-server_") {
			t.Fatalf("%s: %q %v", real, got, err)
		}
		if _, _, err := rewriteBootOriginPull(got, "http://h/bluefin/m"); err != nil {
			t.Fatalf("%s: %v", real, err)
		}
	}
}

func TestRewriteBootOriginPull(t *testing.T) {
	const base = "http://192.168.1.10:8080/bluefin/aa-bb-cc-dd-ee-b1"
	cases := []struct {
		name, in, want, file string
	}{
		{"normal",
			"usrhash=abc root=tmpfs rd.systemd.pull=raw,machine,verify=signature,blockdev,bootorigin:rootdisk:bluefin-server_1.raw console=ttyS0,115200",
			"usrhash=abc root=tmpfs rd.systemd.pull=raw,machine,verify=signature,blockdev:rootdisk:" + base + "/bluefin-server_1.raw console=ttyS0,115200",
			"bluefin-server_1.raw"},
		{"bootorigin first, extra options, odd spacing kept",
			"a=1  rd.systemd.pull=bootorigin,raw,foo=bar,verify=signature:rootdisk:x.raw\tb=\"q r\"",
			"a=1  rd.systemd.pull=raw,foo=bar,verify=signature:rootdisk:" + base + "/x.raw\tb=\"q r\"",
			"x.raw"},
		{"only argument",
			"rd.systemd.pull=raw,bootorigin:disk:img.raw",
			"rd.systemd.pull=raw:disk:" + base + "/img.raw",
			"img.raw"},
		{"another pull without bootorigin is kept",
			"rd.systemd.pull=raw,machine:extra:http://elsewhere/e.raw rd.systemd.pull=raw,bootorigin:rootdisk:r.raw",
			"rd.systemd.pull=raw,machine:extra:http://elsewhere/e.raw rd.systemd.pull=raw:rootdisk:" + base + "/r.raw",
			"r.raw"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, file, err := rewriteBootOriginPull(tc.in, base)
			if err != nil || got != tc.want || file != tc.file {
				t.Fatalf("got %q %q %v\nwant %q %q", got, file, err, tc.want, tc.file)
			}
		})
	}
	for name, in := range map[string]string{
		"missing bootorigin":    "usrhash=abc rd.systemd.pull=raw,machine:rootdisk:http://x/y.raw",
		"no pull at all":        "usrhash=abc root=tmpfs",
		"two bootorigin pulls":  "rd.systemd.pull=raw,bootorigin:a:a.raw rd.systemd.pull=raw,bootorigin:b:b.raw",
		"bootorigin in a value": "x=rd.systemd.pull=raw,bootorigin:a:a.raw",
		"no local name":         "rd.systemd.pull=raw,bootorigin:a.raw",
		"empty file name":       "rd.systemd.pull=raw,bootorigin:rootdisk:",
		"absolute file name":    "rd.systemd.pull=raw,bootorigin:rootdisk:http://x/a.raw",
		"file name with a path": "rd.systemd.pull=raw,bootorigin:rootdisk:dir/a.raw",
		"bootorigin not option": "rd.systemd.pull=raw,machine:bootorigin:a.raw",
	} {
		t.Run(name, func(t *testing.T) {
			if got, _, err := rewriteBootOriginPull(in, base); err == nil {
				t.Fatalf("must be refused, got %q", got)
			}
		})
	}
}

func TestIPXESafeArgs(t *testing.T) {
	if err := ipxeSafeArgs(sampleUKICmdline); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"a=${mac}", "a=\"b c\"", "a='b'", "a=\\n", "a\nb"} {
		if ipxeSafeArgs(bad) == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestBluefinIPXEChainload(t *testing.T) {
	srv, dir := newTestServer(t)
	installBluefinFixture(t, dir)
	writeUKI(t, dir, bluefinTestVersion, map[string][]byte{".linux": []byte("KERNEL"), ".cmdline": padded(sampleUKICmdline)})
	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin"}`)

	r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC, "")
	dirURL := "http://192.168.1.10:8080/bluefin/" + bluefinDashMAC
	wantCmdline := strings.Replace(sampleUKICmdline,
		"rd.systemd.pull=raw,machine,verify=signature,blockdev,bootorigin:rootdisk:bluefin-server_"+bluefinTestVersion+".raw",
		"rd.systemd.pull=raw,machine,verify=signature,blockdev:rootdisk:"+dirURL+"/bluefin-server_"+bluefinTestVersion+".raw", 1)
	for _, want := range []string{
		"#!ipxe\niseq ${platform} pcbios && goto bios ||\niseq ${platform} efi || goto not-efi\niseq ${buildarch} x86_64 || goto not-efi\n",
		"set menu-timeout 5000\n",
		"menu Booty - Bluefin Server " + bluefinTestVersion + " (diskless) - srv1\n",
		"choose --timeout ${menu-timeout} --default netboot selected || goto run-from-disk\n",
		"\nchain " + dirURL + "/bluefin-server-netboot_" + bluefinTestVersion + ".efi " + wantCmdline + " || goto chain-failed\n",
		"item --key d run-from-disk Boot from disk\n", "item --key s shell         iPXE shell\n", "item --key r reboot        Reboot\n",
		":run-from-disk\nexit\n",
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("missing %q:\n%s", want, r.body)
		}
	}
	if strings.Contains(r.body, "[[") || strings.Contains(r.body, "bootorigin") {
		t.Fatalf("bad script:\n%s", r.body)
	}
	if h, _ := hardware.Get(bluefinMAC); h.Booted != "" || h.InstallServedAt != "" {
		t.Fatalf("the iPXE render records nothing; the UKI GET does: %+v", h)
	}

	chainURL := srv.URL + "/bluefin/" + bluefinDashMAC + "/bluefin-server-netboot_" + bluefinTestVersion + ".efi"
	if got := do(t, http.MethodGet, chainURL, ""); got.status != 200 || got.contentType != "application/efi" {
		t.Fatalf("chain URL: %+v", got)
	}
	if h, _ := hardware.Get(bluefinMAC); h.Booted == "" {
		t.Fatal("the chainloaded UKI GET records the boot")
	}
	for _, p := range []string{"/bluefin-server_" + bluefinTestVersion + ".raw", "/SHA256SUMS", "/SHA256SUMS.gpg", "/bluefin-node.ign"} {
		if got := do(t, http.MethodGet, strings.Replace(dirURL, "http://192.168.1.10:8080", srv.URL, 1)+p+"?preview=1", ""); got.status != 200 {
			t.Errorf("the initrd finds %s next to the explicit pull URL: %+v", p, got)
		}
	}

	t.Run("Secure Boot keeps the HTTP Boot menu", func(t *testing.T) {
		r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC+"&sb=1", "")
		if !strings.Contains(r.body, "switch to UEFI HTTP Boot - srv1") || !strings.Contains(r.body, "echo with Secure Boot on, systemd-stub ignores the command line iPXE passes") || strings.Contains(r.body, "\nchain ") {
			t.Fatalf("%s", r.body)
		}
		r = do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC+"&preview=1", "")
		if strings.Contains(r.body, "\nchain ") {
			t.Fatal("a preview of a host last seen through Secure Boot does not chainload either")
		}
		do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC, "")
	})

	t.Run("installed host boots its disk, reinstalling one chainloads", func(t *testing.T) {
		register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","mode":"installed"}`)
		r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC, "")
		if r.body != "#!ipxe\necho Booty: srv1 is an installed Bluefin Server host; booting from disk\nexit\n" {
			t.Fatalf("%q", r.body)
		}
		register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","mode":"installed","doInstall":true,"installDisk":"/dev/sda"}`)
		if r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC, ""); !strings.Contains(r.body, "\nchain ") {
			t.Fatalf("%s", r.body)
		}
		register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin"}`)
	})

	t.Run("the command line is cached per UKI and follows a changed file", func(t *testing.T) {
		path := filepath.Join(dir, "bluefin", bluefinTestVersion, "bluefin-server-netboot_"+bluefinTestVersion+".efi")
		info, _ := os.Stat(path)
		if err := os.WriteFile(path, synthPE(t, map[string][]byte{".cmdline": padded(strings.Replace(sampleUKICmdline, "console=tty0", "console=tty1", 1))}), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, info.ModTime().Add(time.Second), info.ModTime().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC, ""); !strings.Contains(r.body, "console=tty1") {
			t.Fatalf("%s", r.body)
		}
	})

	t.Run("fail closed", func(t *testing.T) {
		for name, sections := range map[string]map[string][]byte{
			"no .cmdline":         {".linux": []byte("K")},
			"no bootorigin pull":  {".cmdline": padded("usrhash=abc rd.systemd.pull=raw:rootdisk:http://x/bluefin-server_" + bluefinTestVersion + ".raw")},
			"two bootorigin":      {".cmdline": padded("rd.systemd.pull=raw,bootorigin:a:bluefin-server_" + bluefinTestVersion + ".raw rd.systemd.pull=raw,bootorigin:b:b.raw")},
			"another release DDI": {".cmdline": padded("rd.systemd.pull=raw,bootorigin:rootdisk:bluefin-server_1.raw")},
			"iPXE metacharacter":  {".cmdline": padded("a=${x} rd.systemd.pull=raw,bootorigin:rootdisk:bluefin-server_" + bluefinTestVersion + ".raw")},
		} {
			t.Run(name, func(t *testing.T) {
				writeUKI(t, dir, bluefinTestVersion, sections)
				path := filepath.Join(dir, "bluefin", bluefinTestVersion, "bluefin-server-netboot_"+bluefinTestVersion+".efi")
				later := time.Now().Add(time.Duration(len(name)) * time.Hour)
				if err := os.Chtimes(path, later, later); err != nil {
					t.Fatal(err)
				}
				r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC, "")
				if strings.Contains(r.body, "\nchain ") || !strings.Contains(r.body, "switch to UEFI HTTP Boot - srv1") || !strings.Contains(r.body, "no usable command line") {
					t.Fatalf("%s", r.body)
				}
			})
		}
	})

	t.Run("no release cached", func(t *testing.T) {
		if err := os.Remove(filepath.Join(dir, "bluefin", "current")); err != nil {
			t.Fatal(err)
		}
		r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC, "")
		if strings.Contains(r.body, "\nchain ") || !strings.Contains(r.body, "no Bluefin Server release is cached yet") {
			t.Fatalf("%s", r.body)
		}
	})
}

func TestBluefinVersionedUKIRoute(t *testing.T) {
	srv, dir := newTestServer(t)
	writeBluefinRelease(t, dir, bluefinPrevVersion, "previous")
	installBluefinFixture(t, dir)
	viper.Set(config.AutoRegister, "")
	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin"}`)
	base := srv.URL + "/bluefin/" + bluefinDashMAC + "/"
	for name, want := range map[string]string{
		"bluefin-server-netboot_" + bluefinTestVersion + ".efi": "UKI-" + bluefinTestVersion,
		"bluefin-server-netboot_" + bluefinPrevVersion + ".efi": "UKI-" + bluefinPrevVersion,
		"bluefin-server-netboot.efi":                            "UKI-" + bluefinTestVersion,
		"bluefin-server-netboot_20250101.1.efi":                 "",
	} {
		r := do(t, http.MethodGet, base+name, "")
		if want == "" {
			if r.status != 404 {
				t.Errorf("%s: an unknown release is 404: %+v", name, r)
			}
			continue
		}
		if r.status != 200 || r.body != want {
			t.Errorf("%s: %+v", name, r)
		}
	}
}
