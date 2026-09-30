package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/versions"
)

func addNvidiaSysexts(t *testing.T, dir, version string) {
	t.Helper()
	rel := filepath.Join(dir, "bluefin", version)
	m, err := versions.LoadBluefinManifest(rel)
	if err != nil {
		t.Fatal(err)
	}
	for name, file := range map[string]string{
		"nvidia-open-595":          "nvidia-open-595_" + version + ".raw",
		"nvidia-container-toolkit": "nvidia-container-toolkit-1.20.1.raw",
	} {
		body := strings.ToUpper(name) + "-" + version
		if err := os.WriteFile(filepath.Join(rel, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		m.Sysexts[name] = versions.BluefinSysext{File: file, Sha256: sha256Hex(body)}
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rel, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBluefinNodeIgnitionNvidiaExtensions(t *testing.T) {
	srv, dir := newTestServer(t)
	installBluefinFixture(t, dir)
	addNvidiaSysexts(t, dir, bluefinTestVersion)
	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"gpu1","os":"bluefin","extensions":["nvidia-open-595","nvidia-container-toolkit"]}`)

	_, files, units := bluefinNode(t, srv.URL, bluefinMAC, "")
	base := "http://192.168.1.10:8080/data/bluefin/" + bluefinTestVersion + "/"
	want := map[string]bluefinNodeFile{
		"/etc/extensions/nvidia-open-595_" + bluefinTestVersion + ".raw": {mode: 0o644, source: base + "nvidia-open-595_" + bluefinTestVersion + ".raw", hash: "sha256-" + sha256Hex("NVIDIA-OPEN-595-"+bluefinTestVersion), overwrite: true, remote: true},
		"/etc/extensions/nvidia-container-toolkit.raw":                   {mode: 0o644, source: base + "nvidia-container-toolkit-1.20.1.raw", hash: "sha256-" + sha256Hex("NVIDIA-CONTAINER-TOOLKIT-"+bluefinTestVersion), overwrite: true, remote: true},
	}
	delete(files, "/etc/hostname")
	if len(files) != len(want) || len(units) != 0 {
		t.Fatalf("only the two sysexts are added: files %v units %v", files, units)
	}
	for p, w := range want {
		if got := files[p]; got != w {
			t.Errorf("%s:\n got %+v\nwant %+v", p, got, w)
		}
		r := do(t, http.MethodGet, strings.Replace(w.source, "http://192.168.1.10:8080", srv.URL, 1), "")
		if r.status != 200 || "sha256-"+sha256Hex(r.body) != w.hash {
			t.Errorf("%s: the source must serve the verified bytes: %+v", p, r)
		}
	}
}

func TestBluefinNodeIgnitionNvidiaMissingFromRelease(t *testing.T) {
	srv, dir := newTestServer(t)
	installBluefinFixture(t, dir)
	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"gpu1","os":"bluefin","extensions":["nvidia-open-595","nvidia-container-toolkit"]}`)

	_, files, _ := bluefinNode(t, srv.URL, bluefinMAC, "")
	for p := range files {
		if strings.Contains(p, "nvidia") {
			t.Fatalf("a sysext the release lacks is left out: %v", files)
		}
	}
}

func TestBluefinRegisterNvidiaExtensions(t *testing.T) {
	srv, _ := newTestServer(t)
	for body, want := range map[string]string{
		`"extensions":["nvidia-open-595","nvidia-open-615"]`: "only one NVIDIA driver flavour per host",
		`"extensions":["nvidia-open-595","zfs"]`:             "nvidia-open-595 and zfs cannot be merged",
		`"extensions":["nvidia-open"]`:                       "is not one of",
	} {
		r := do(t, http.MethodPost, srv.URL+"/register", `{"mac":"`+bluefinMAC+`","os":"bluefin",`+body+`}`)
		if r.status != http.StatusBadRequest || !strings.Contains(r.body, want) {
			t.Errorf("%s: want 400 %q, got %+v", body, want, r)
		}
	}
}
