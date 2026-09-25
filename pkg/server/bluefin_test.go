package server

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/creds"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/state"
	"github.com/jeefy/booty/pkg/versions"
	"github.com/spf13/viper"
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

var credsArgRe = regexp.MustCompile(`inst\.creds_url=(\S+) inst\.creds_sha256=([0-9a-f]{64})`)

func TestBluefinIPXEAndCreds(t *testing.T) {
	srv, dir := newTestServer(t)
	viper.Set(config.SSHAuthorizedKeys, []string{"ssh-ed25519 AAAA1 a"})

	r := do(t, http.MethodPost, srv.URL+"/register", `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","doInstall":true,"installDisk":"/dev/vda"}`)
	if r.status != 200 {
		t.Fatalf("register: %+v", r)
	}

	r = do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC, "")
	if r.status != 200 || !strings.Contains(r.body, "echo Bluefin artifacts not downloaded yet") || strings.Contains(r.body, "kernel ") || strings.Contains(r.body, "[[") {
		t.Fatalf("without a cached release the pending menu is served: %+v", r)
	}
	if h, _ := hardware.Get(bluefinMAC); h.InstallServedAt != "" || !h.DoInstall {
		t.Fatalf("pending menu must not stamp installServedAt: %+v", h)
	}

	installBluefinFixture(t, dir)
	r = do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC, "")
	if r.status != 200 || strings.Contains(r.body, "[[") {
		t.Fatalf("bluefin ipxe: %+v", r)
	}
	for _, want := range []string{
		"iseq ${platform} efi || goto not-efi",
		"set BASEURL http://192.168.1.10:8080/data/bluefin/current",
		"menu Booty - Bluefin Server 26.08.0 - srv1",
		"Install Bluefin Server to /dev/vda (wipes it)",
		"--default install selected",
		"kernel ${BASEURL}/bluefin-server-pxe-vmlinuz-26.08.0 systemd.unit=system-install.target",
		"inst.ddi_url=${BASEURL}/bluefin-server-ddi-4593.2.5.raw.zst inst.ddi_sha256=3498541fce7e25c1bc8eb73bf6ce624af60e0a99d1f0637068284f6b7c8cd8a3",
		"inst.target_disk=/dev/vda",
		"inst.creds_url=http://192.168.1.10:8080/creds/" + bluefinMAC + ".tar inst.creds_sha256=",
		"initrd ${BASEURL}/bluefin-server-pxe-initrd-26.08.0.cpio.gz",
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("script missing %q:\n%s", want, r.body)
		}
	}
	h, _ := hardware.Get(bluefinMAC)
	if h.InstallServedAt == "" || !h.DoInstall {
		t.Fatalf("serving the install stanza must stamp installServedAt and keep doInstall: %+v", h)
	}

	m := credsArgRe.FindStringSubmatch(r.body)
	if m == nil {
		t.Fatalf("no creds args in script:\n%s", r.body)
	}
	credsPath := strings.TrimPrefix(m[1], "http://192.168.1.10:8080")
	scriptSum := m[2]

	resp, err := http.Get(srv.URL + credsPath)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/x-tar" || resp.ContentLength != int64(len(body)) {
		t.Fatalf("creds tar: %d %v %v", resp.StatusCode, resp.Header, err)
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != scriptSum {
		t.Fatalf("served tar sha256 %s differs from inst.creds_sha256 %s", got, scriptSum)
	}
	r = do(t, http.MethodGet, srv.URL+credsPath+".sha256", "")
	if r.status != 200 || r.body != scriptSum+"\n" || !strings.HasPrefix(r.contentType, "text/plain") {
		t.Fatalf("creds sha256 endpoint: %+v want %s", r, scriptSum)
	}

	names := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(body))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content, _ := io.ReadAll(tr)
		plain, err := creds.Decrypt(strings.TrimSuffix(hdr.Name, ".cred"), content)
		if err != nil {
			t.Fatalf("%s must be a null-key encrypted credential named after the file: %v", hdr.Name, err)
		}
		names[hdr.Name] = string(plain)
	}
	if names["firstboot.hostname.cred"] != "srv1\n" {
		t.Fatalf("hostname credential: %q (entries %v)", names["firstboot.hostname.cred"], names)
	}
	rules := names["tmpfiles.extra.cred"]
	for _, want := range []string{
		"f+ /etc/hostname 0644 root root - srv1\n",
		"L+ /etc/systemd/system/sysinit.target.wants/booty-hostname.service - - - - /etc/systemd/system/booty-hostname.service\n",
		"/home/core/.ssh/authorized_keys",
		"L+ /etc/systemd/system/multi-user.target.wants/booty-booted.service - - - - /etc/systemd/system/booty-booted.service\n",
		"L+ /etc/systemd/system/timers.target.wants/booty-update.timer - - - - /etc/systemd/system/booty-update.timer\n",
		"/opt/booty/update-check",
	} {
		if !strings.Contains(rules, want) {
			t.Fatalf("tmpfiles rules missing %q:\n%s", want, rules)
		}
	}
	if len(names) != 2 {
		t.Fatalf("bundle must hold exactly firstboot.hostname and tmpfiles.extra, got %v", names)
	}

	assertJSONError(t, do(t, http.MethodGet, srv.URL+"/creds/aa:bb:cc:dd:ee:99.tar", ""), http.StatusNotFound)
	assertJSONError(t, do(t, http.MethodGet, srv.URL+"/creds/nope.tar", ""), http.StatusBadRequest)
	assertJSONError(t, do(t, http.MethodGet, srv.URL+"/creds/"+bluefinMAC+".zip", ""), http.StatusNotFound)
	assertJSONError(t, do(t, http.MethodGet, srv.URL+"/creds/", ""), http.StatusNotFound)
	assertJSONError(t, do(t, http.MethodPost, srv.URL+credsPath, ""), http.StatusMethodNotAllowed)

	viper.Set(config.Builtin, "none")
	r = do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC, "")
	if strings.Contains(r.body, "inst.creds_") {
		t.Fatalf("--builtin=none must not advertise a credentials bundle:\n%s", r.body)
	}

	viper.Set(config.Builtin, config.DefaultBuiltin)
	r = do(t, http.MethodPost, srv.URL+"/register", `{"mac":"aa:bb:cc:dd:ee:b2","hostname":"srv2","os":"bluefin"}`)
	if r.status != 200 {
		t.Fatalf("register: %+v", r)
	}
	r = do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac=aa:bb:cc:dd:ee:b2", "")
	if !strings.Contains(r.body, "--default run-from-disk") || strings.Contains(r.body, "inst.target_disk") || !strings.Contains(r.body, "to the first writable disk") {
		t.Fatalf("host without doInstall/installDisk: %+v", r)
	}
	if h, _ := hardware.Get("aa:bb:cc:dd:ee:b2"); h.InstallServedAt != "" {
		t.Fatalf("run-from-disk default must not stamp installServedAt: %+v", h)
	}
}

func TestDoInstallClearOnNextBoot(t *testing.T) {
	srv, dir := newTestServer(t)
	installBluefinFixture(t, dir)
	viper.Set(config.DoInstallClearOn, config.ClearOnNextBoot)
	viper.Set(config.InstallMinDuration, 3*time.Minute)

	register(t, srv.URL, `{"mac":"`+bluefinMAC+`","hostname":"srv1","os":"bluefin","doInstall":true}`)

	get := func() (string, *hardware.Host) {
		t.Helper()
		r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+bluefinMAC, "")
		if r.status != 200 {
			t.Fatalf("ipxe: %+v", r)
		}
		h, _ := hardware.Get(bluefinMAC)
		return r.body, h
	}

	body, h := get()
	if !strings.Contains(body, "--default install") || !h.DoInstall || h.InstallServedAt == "" {
		t.Fatalf("first fetch serves install and stamps installServedAt: %+v", h)
	}
	first := h.InstallServedAt

	body, h = get()
	if !strings.Contains(body, "--default install") || !h.DoInstall {
		t.Fatalf("a re-PXE within installMinDuration keeps install (installer crashed): %+v", h)
	}

	old := time.Now().Add(-4 * time.Minute).UTC().Format(time.RFC3339)
	if _, err := hardware.Update(bluefinMAC, func(h *hardware.Host) { h.InstallServedAt = old }); err != nil {
		t.Fatal(err)
	}
	body, h = get()
	if !strings.Contains(body, "--default run-from-disk") || h.DoInstall || h.InstallServedAt != "" {
		t.Fatalf("a re-PXE after installMinDuration clears doInstall and serves run-from-disk: %+v\n%s", h, body)
	}

	body, h = get()
	if !strings.Contains(body, "--default run-from-disk") || h.DoInstall {
		t.Fatalf("cleared stays cleared: %+v", h)
	}

	viper.Set(config.DoInstallClearOn, config.ClearOnBooted)
	if _, err := hardware.Update(bluefinMAC, func(h *hardware.Host) { h.DoInstall, h.InstallServedAt = true, old }); err != nil {
		t.Fatal(err)
	}
	body, h = get()
	if !strings.Contains(body, "--default install") || !h.DoInstall || h.InstallServedAt == old || h.InstallServedAt < first {
		t.Fatalf("other modes never clear on /booty.ipxe but still refresh the stamp: %+v", h)
	}
	r := do(t, http.MethodPost, srv.URL+"/booted?mac="+bluefinMAC, "")
	if r.status != 200 {
		t.Fatalf("booted: %+v", r)
	}
	if h, _ = hardware.Get(bluefinMAC); h.DoInstall || h.InstallServedAt != "" {
		t.Fatalf("POST /booted clears doInstall and installServedAt: %+v", h)
	}

	viper.Set(config.DoInstallClearOn, config.ClearOnNextBoot)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:c1","hostname":"fc","os":"flatcar","doInstall":true}`)
	if _, err := hardware.Update("aa:bb:cc:dd:ee:c1", func(h *hardware.Host) { h.InstallServedAt = old }); err != nil {
		t.Fatal(err)
	}
	do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac=aa:bb:cc:dd:ee:c1", "")
	do(t, http.MethodGet, srv.URL+"/ignition.json?mac=aa:bb:cc:dd:ee:c1", "")
	if h, _ := hardware.Get("aa:bb:cc:dd:ee:c1"); !h.DoInstall {
		t.Fatalf("next-boot only applies to bluefin; flatcar keeps doInstall until /booted: %+v", h)
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
