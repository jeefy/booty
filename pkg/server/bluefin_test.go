package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/state"
	"github.com/jeefy/booty/pkg/versions"
)

const bluefinMAC = "aa:bb:cc:dd:ee:b1"

func installBluefinFixture(t *testing.T, dir string) {
	t.Helper()
	rel := filepath.Join(dir, "bluefin", "26.08.0")
	if err := os.MkdirAll(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"version":"26.08.0","vmlinuz":"bluefin-server-pxe-vmlinuz-26.08.0","initrd":"bluefin-server-pxe-initrd-26.08.0.cpio.gz","ddi":"bluefin-server-ddi-4593.2.5.raw.zst","ddiSha256":"3498541fce7e25c1bc8eb73bf6ce624af60e0a99d1f0637068284f6b7c8cd8a3"}`
	if err := os.WriteFile(filepath.Join(rel, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("26.08.0", filepath.Join(dir, "bluefin", "current")); err != nil {
		t.Fatal(err)
	}
	state.SetCurrentBluefinVersion("26.08.0")
	t.Cleanup(func() { state.SetCurrentBluefinVersion("") })
}

func TestBluefinIPXEMenu(t *testing.T) {
	srv, dir := newTestServer(t)
	installBluefinFixture(t, dir)
	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","doInstall":true,"installDisk":"/dev/vda"}`)

	for _, q := range []string{"", "&sb=1"} {
		r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC+q, "")
		if r.status != 200 || !strings.Contains(r.body, "switch to UEFI HTTP Boot - srv1") || !strings.Contains(r.body, "echo   http://192.168.1.10:8080/bluefin/aa-bb-cc-dd-ee-b1/bluefin-server-netboot.efi\n") {
			t.Fatalf("%q: %+v", q, r)
		}
		if strings.Contains(r.body, "\nkernel ") || strings.Contains(r.body, "inst.") || strings.Contains(r.body, "[[") {
			t.Fatalf("%q: iPXE never boots Bluefin: %s", q, r.body)
		}
	}
	if h, _ := hardware.Get(bluefinMAC); !h.DoInstall || h.InstallServedAt != "" {
		t.Fatalf("the iPXE menu leaves doInstall alone: %+v", h)
	}
	if r := do(t, http.MethodGet, srv.URL+"/creds/"+bluefinMAC+".tar", ""); r.status != http.StatusNotFound {
		t.Fatalf("the installer credentials bundle is gone: %+v", r)
	}
}

func TestUpdateCheckBluefin(t *testing.T) {
	srv, _ := newTestServer(t)
	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:c1","hostname":"fc","os":"flatcar"}`)
	state.SetCurrentFlatcarVersion("2.0.0")
	t.Cleanup(func() { state.SetCurrentFlatcarVersion("") })

	get := func(query string) updateCheckResponse {
		t.Helper()
		r := do(t, http.MethodGet, srv.URL+"/update-check?"+query, "")
		if r.status != 200 {
			t.Fatalf("update-check: %+v", r)
		}
		var resp updateCheckResponse
		if err := json.Unmarshal([]byte(r.body), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := get("mac=" + bluefinMAC + "&os=bluefin&version=26.08.0")
	if resp.RebootRequired || resp.Reason != bluefinUpdateReason || resp.Running != "26.08.0" || resp.Target != "" {
		t.Fatalf("bluefin never reboots: %+v", resp)
	}
	h, _ := hardware.Get(bluefinMAC)
	if h.Running != "26.08.0" || h.LastCheck == "" || h.RebootPending {
		t.Fatalf("check must still be recorded: %+v", h)
	}

	resp = get("mac=" + bluefinMAC + "&os=flatcar&version=1.0.0")
	if resp.RebootRequired || resp.Reason != bluefinUpdateReason {
		t.Fatalf("a bluefin host reporting os=flatcar (Flatcar-based os-release) is still bluefin: %+v", resp)
	}

	resp = get("mac=aa:bb:cc:dd:ee:c1&os=bluefin&version=1.0.0")
	if resp.RebootRequired || resp.Reason != bluefinUpdateReason {
		t.Fatalf("os=bluefin from the client wins over the registered os: %+v", resp)
	}
	resp = get("mac=aa:bb:cc:dd:ee:c1&os=flatcar&version=1.0.0")
	if !resp.RebootRequired {
		t.Fatalf("flatcar logic untouched: %+v", resp)
	}
}

func TestBluefinManifestIsServedFromData(t *testing.T) {
	srv, dir := newTestServer(t)
	installBluefinFixture(t, dir)
	if m, ok := versions.CurrentBluefinManifest(); !ok || m.Version != "26.08.0" {
		t.Fatalf("manifest: %+v %v", m, ok)
	}
	r := do(t, http.MethodGet, srv.URL+"/data/bluefin/current/manifest.json", "")
	if r.status != 200 || !strings.Contains(r.body, `"ddiSha256"`) {
		t.Fatalf("manifest is public: %+v", r)
	}
}
