package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	ign "github.com/jeefy/booty/pkg/ignition"
	"github.com/jeefy/booty/pkg/state"
	"github.com/spf13/viper"
)

func writeRelease(t *testing.T, dir, osName, version string, files ...string) {
	t.Helper()
	rel := filepath.Join(dir, osName, version)
	if err := os.MkdirAll(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(rel, f), []byte(f+"-"+version), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTargetVersionFlatcarRender(t *testing.T) {
	srv, dir := newTestServer(t)
	state.SetCurrentFlatcarVersion("4757.2.0")
	t.Cleanup(func() { state.SetCurrentFlatcarVersion("") })
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"f1","os":"flatcar"}`)

	r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac=aa:bb:cc:dd:ee:01", "")
	if !strings.Contains(r.body, "kernel http://192.168.1.10:8080/data/flatcar_production_pxe.vmlinuz ") {
		t.Fatalf("without a release directory the top-level names are used:\n%s", r.body)
	}
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/register", `{"mac":"aa:bb:cc:dd:ee:01","hostname":"f1","os":"flatcar","targetVersion":"4593.2.1"}`), http.StatusBadRequest)
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/register", `{"mac":"aa:bb:cc:dd:ee:01","hostname":"f1","os":"flatcar","targetVersion":"../etc"}`), http.StatusBadRequest)

	for _, v := range []string{"4757.2.0", "4593.2.1"} {
		writeRelease(t, dir, "flatcar", v, "flatcar_production_pxe.vmlinuz", "flatcar_production_pxe_image.cpio.gz")
	}
	r = do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac=aa:bb:cc:dd:ee:01", "")
	for _, want := range []string{
		"kernel http://192.168.1.10:8080/data/flatcar/4757.2.0/flatcar_production_pxe.vmlinuz flatcar.first_boot=1",
		"initrd http://192.168.1.10:8080/data/flatcar/4757.2.0/flatcar_production_pxe_image.cpio.gz",
	} {
		if !strings.Contains(r.body, want) {
			t.Fatalf("fleet target render missing %q:\n%s", want, r.body)
		}
	}
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"f1","os":"flatcar","targetVersion":"4593.2.1"}`)
	r = do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac=aa:bb:cc:dd:ee:01", "")
	if !strings.Contains(r.body, "/data/flatcar/4593.2.1/flatcar_production_pxe.vmlinuz") || !strings.Contains(r.body, "/data/flatcar/4593.2.1/flatcar_production_pxe_image.cpio.gz") || strings.Contains(r.body, "4757.2.0") {
		t.Fatalf("targetVersion render:\n%s", r.body)
	}
	h := do(t, http.MethodGet, srv.URL+"/hosts?mac=aa:bb:cc:dd:ee:01", "")
	if !strings.Contains(h.body, `"targetVersion":"4593.2.1"`) {
		t.Fatalf("/hosts shows targetVersion: %+v", h)
	}
	if r := do(t, http.MethodGet, srv.URL+"/data/flatcar/4593.2.1/flatcar_production_pxe.vmlinuz", ""); r.status != 200 || r.body != "flatcar_production_pxe.vmlinuz-4593.2.1" {
		t.Fatalf("/data/ serves the versioned path: %+v", r)
	}
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/register", `{"mac":"aa:bb:cc:dd:ee:02","hostname":"f2","os":"flatcar","targetVersion":"20260927.123"}`), http.StatusBadRequest)
}

func TestTargetVersionCoreOSRender(t *testing.T) {
	srv, dir := newTestServer(t)
	state.SetCurrentCoreOSVersion("44.20260913.2.1")
	t.Cleanup(func() { state.SetCurrentCoreOSVersion("") })
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:03","hostname":"c1","os":"coreos"}`)
	r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac=aa:bb:cc:dd:ee:03", "")
	if !strings.Contains(r.body, "set BASEURL http://192.168.1.10:8080/data/\n") || !strings.Contains(r.body, "set VERSION 44.20260913.2.1\n") {
		t.Fatalf("without a release directory the top-level names are used:\n%s", r.body)
	}
	for _, v := range []string{"44.20260913.2.1", "44.20260801.1.0"} {
		writeRelease(t, dir, "coreos", v, "fedora-coreos-"+v+"-live-kernel-x86_64")
	}
	r = do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac=aa:bb:cc:dd:ee:03", "")
	if !strings.Contains(r.body, "set BASEURL http://192.168.1.10:8080/data/coreos/44.20260913.2.1\n") || !strings.Contains(r.body, "set VERSION 44.20260913.2.1\n") {
		t.Fatalf("fleet target render:\n%s", r.body)
	}
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:03","hostname":"c1","os":"coreos","targetVersion":"44.20260801.1.0"}`)
	r = do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac=aa:bb:cc:dd:ee:03", "")
	if !strings.Contains(r.body, "set BASEURL http://192.168.1.10:8080/data/coreos/44.20260801.1.0\n") || !strings.Contains(r.body, "set VERSION 44.20260801.1.0\n") || strings.Contains(r.body, "44.20260913.2.1") {
		t.Fatalf("targetVersion render:\n%s", r.body)
	}
	if r := do(t, http.MethodGet, srv.URL+"/data/coreos/44.20260801.1.0/fedora-coreos-44.20260801.1.0-live-kernel-x86_64", ""); r.status != 200 {
		t.Fatalf("/data/ serves the versioned path: %+v", r)
	}
}

// prevUKI is a netboot UKI of the previous fixture release whose command
// line pulls that release's DDI.
func prevUKI() map[string][]byte {
	s := biosUKI(false)
	s[".cmdline"] = padded(strings.ReplaceAll(sampleUKICmdline, bluefinTestVersion, bluefinPrevVersion))
	return s
}

func TestTargetVersionBluefinRender(t *testing.T) {
	srv, dir := newTestServer(t)
	writeBluefinRelease(t, dir, bluefinPrevVersion, "previous")
	installBluefinFixture(t, dir)
	writeUKI(t, dir, bluefinTestVersion, biosUKI(true))
	writeUKI(t, dir, bluefinPrevVersion, prevUKI())
	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","extensions":["zfs"],"doInstall":true,"installDisk":"/dev/vda"}`)
	dirURL := "http://192.168.1.10:8080/bluefin/" + bluefinDashMAC

	check := func(t *testing.T, version, other string) {
		t.Helper()
		r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC, "")
		for _, want := range []string{
			"menu Booty - Bluefin Server " + version + " (diskless) - srv1",
			"chain " + dirURL + "/bluefin-server-netboot_" + version + ".efi usrhash=",
			"blockdev:rootdisk:" + dirURL + "/bluefin-server_" + version + ".raw ",
			"kernel " + dirURL + "/bluefin-server-netboot_" + version + ".linux usrhash=",
			"initrd " + dirURL + "/bluefin-server-netboot_" + version + ".initrd || goto bios-failed",
		} {
			if !strings.Contains(r.body, want) {
				t.Fatalf("iPXE render for %s missing %q:\n%s", version, want, r.body)
			}
		}
		if strings.Contains(r.body, other) {
			t.Fatalf("iPXE render for %s must not mention %s:\n%s", version, other, r.body)
		}
		uki, err := os.ReadFile(filepath.Join(dir, "bluefin", version, "bluefin-server-netboot_"+version+".efi"))
		if err != nil {
			t.Fatal(err)
		}
		if r := do(t, http.MethodGet, srv.URL+bluefinBootPath+"?preview=1", ""); r.status != 200 || r.body != string(uki) {
			t.Fatalf("unversioned UKI is the target release's (%s): status %d, %d bytes", version, r.status, len(r.body))
		}
		if r := do(t, http.MethodGet, srv.URL+"/bluefin/"+bluefinDashMAC+"/SHA256SUMS", ""); r.body != "SUMS-"+version {
			t.Fatalf("SHA256SUMS is the target release's: %+v", r)
		}
		_, files, units := bluefinNode(t, srv.URL, bluefinMAC, "?preview=1")
		zfs := files["/etc/extensions/zfs_"+version+".raw"]
		if zfs.source != "http://192.168.1.10:8080/data/bluefin/"+version+"/zfs_"+version+".raw" {
			t.Fatalf("zfs sysext for %s: %+v (files %v)", version, zfs, files)
		}
		if inst := units[bluefinInstallUnit]; inst.Contents == nil || !strings.Contains(*inst.Contents, "bluefin-server-"+version+".efi") {
			t.Fatalf("install unit for %s: %+v", version, inst)
		}
	}
	check(t, bluefinTestVersion, bluefinPrevVersion)

	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","extensions":["zfs"],"doInstall":true,"installDisk":"/dev/vda","targetVersion":"`+bluefinPrevVersion+`"}`)
	check(t, bluefinPrevVersion, bluefinTestVersion)

	for name, want := range map[string]string{
		"bluefin-server_" + bluefinTestVersion + ".raw": "DDI-" + bluefinTestVersion,
		"bluefin-server_" + bluefinPrevVersion + ".raw": "DDI-" + bluefinPrevVersion,
	} {
		if r := do(t, http.MethodGet, srv.URL+"/bluefin/"+bluefinDashMAC+"/"+name+"?preview=1", ""); r.status != 200 || r.body != want {
			t.Fatalf("versioned names of every cached release stay reachable: %s %+v", name, r)
		}
	}
	if r := do(t, http.MethodGet, srv.URL+"/bluefin/"+bluefinDashMAC+"/bluefin-server-netboot_"+bluefinTestVersion+".efi?preview=1", ""); r.status != 200 || len(r.body) == 0 {
		t.Fatalf("the current release's versioned UKI stays reachable for a host targeting previous: %d", r.status)
	}
	assertJSONError(t, do(t, http.MethodPost, srv.URL+"/register", `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","targetVersion":"20250101.1"}`), http.StatusBadRequest)
}

func TestBluefinNodeCarriesBootyUnits(t *testing.T) {
	srv, dir := newTestServer(t)
	installBluefinFixture(t, dir)
	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin"}`)

	_, files, units := bluefinNodeAll(t, srv.URL, bluefinMAC, "?preview=1")
	for _, name := range bluefinBootyUnits {
		u, ok := units[name]
		if !ok || u.Contents == nil {
			t.Fatalf("%s missing from the node config: %v", name, units)
		}
		if enabled := u.Enabled != nil && *u.Enabled; enabled == (name == ign.UpdateServiceName) {
			t.Fatalf("%s enabled=%v", name, enabled)
		}
	}
	if !strings.Contains(*units[ign.BootedUnitName].Contents, "http://192.168.1.10:8080/booted?mac=$$MAC") {
		t.Fatalf("booted unit: %s", *units[ign.BootedUnitName].Contents)
	}
	if !strings.Contains(*units[ign.UpdateServiceName].Contents, "ExecStart="+bluefinUpdateCheckScript+"\n") || !strings.Contains(*units[ign.HealthUnitName].Contents, "ExecStart="+bluefinHealthReportScript+"\n") {
		t.Fatalf("units must run the /etc/booty scripts: %v", units)
	}
	for _, p := range bluefinBootyFiles {
		f, ok := files[p]
		if !ok || f.mode != 0o755 || !f.overwrite || !strings.HasPrefix(f.contents, "#!/bin/bash\n") {
			t.Fatalf("%s: %+v", p, f)
		}
	}
	if !strings.Contains(files[bluefinUpdateCheckScript].contents, `"http://192.168.1.10:8080/update-check"`) || !strings.Contains(files[bluefinHealthReportScript].contents, `"http://192.168.1.10:8080/health?mac=$MAC"`) {
		t.Fatalf("scripts must talk to Booty: %v", files)
	}

	viper.Set(config.Builtin, "hostname,health")
	_, files, units = bluefinNodeAll(t, srv.URL, bluefinMAC, "?preview=1")
	if _, ok := units[ign.HealthUnitName]; !ok || len(units) != 1 || len(files) != 2 {
		t.Fatalf("--builtin toggles apply to the node config: %v %v", units, files)
	}
	viper.Set(config.Builtin, "none")
	if r := do(t, http.MethodGet, srv.URL+bluefinNodePath+"?preview=1", ""); r.status != 404 {
		t.Fatalf("--builtin=none with nothing else to configure is 404: %+v", r)
	}
}
