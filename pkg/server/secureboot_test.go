package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

const flatcarCASha = "ebb170da86aa56bae7abd15214c6ee48171d4bde8bc437400e16752c4925dba2"

func setupSecureBoot(t *testing.T, dir, trusted string) {
	t.Helper()
	viper.Set(config.SecureBoot, true)
	viper.Set(config.SecureBootTrusted, trusted)
	t.Cleanup(func() {
		viper.Set(config.SecureBoot, false)
		viper.Set(config.SecureBootTrusted, "")
	})
	sbDir := filepath.Join(dir, "secureboot")
	if err := os.MkdirAll(sbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ca := `{"flatcarVersion":"4757.2.0","sha256":"` + flatcarCASha + `","subject":"CN=Flatcar CA","notAfter":"2037-01-19","source":"x"}`
	if err := os.WriteFile(filepath.Join(sbDir, "flatcar-ca.json"), []byte(ca), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hostSecureBoot(t *testing.T, url, mac string) bool {
	t.Helper()
	r := do(t, http.MethodGet, url+"/hosts?mac="+mac, "")
	if r.status != 200 {
		t.Fatalf("/hosts: %+v", r)
	}
	var h hardware.Host
	if err := json.Unmarshal([]byte(r.body), &h); err != nil {
		t.Fatal(err)
	}
	return h.SecureBoot
}

func TestIPXESecureBootFlagFollowsThePath(t *testing.T) {
	srv, dir := newTestServer(t)
	setupSecureBoot(t, dir, "")
	const mac = "aa:bb:cc:dd:ee:51"
	register(t, srv.URL, `{"mac":"`+mac+`","hostname":"sb-coreos","os":"coreos"}`)
	if hostSecureBoot(t, srv.URL, mac) {
		t.Fatal("fresh host must not be flagged")
	}

	r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+mac+"&sb=1&preview=1", "")
	if r.status != 200 || !strings.Contains(r.body, "shim http://192.168.1.10:8080/boot/secureboot/fedora/shimx64.efi") {
		t.Fatalf("preview must still render the sb variant: %+v", r)
	}
	if hostSecureBoot(t, srv.URL, mac) {
		t.Fatal("preview=1 must not record the flag")
	}

	r = do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+mac+"&sb=1", "")
	if r.status != 200 || !strings.Contains(r.body, "shim http://") {
		t.Fatalf("sb coreos: %+v", r)
	}
	if !hostSecureBoot(t, srv.URL, mac) {
		t.Fatal("sb=1 must set secureBoot")
	}
	data, err := os.ReadFile(filepath.Join(dir, "hardware.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"secureBoot": true`) {
		t.Fatalf("flag not persisted:\n%s", data)
	}

	r = do(t, http.MethodGet, srv.URL+"/booty.json", "")
	var all struct {
		Hosts map[string]struct {
			SecureBoot bool `json:"secureBoot"`
		} `json:"hosts"`
	}
	if err := json.Unmarshal([]byte(r.body), &all); err != nil {
		t.Fatal(err)
	}
	if !all.Hosts[mac].SecureBoot {
		t.Fatalf("/booty.json must expose secureBoot: %s", r.body)
	}
	r = do(t, http.MethodGet, srv.URL+"/cluster", "")
	var cl struct {
		Hosts []struct {
			MAC        string `json:"mac"`
			SecureBoot bool   `json:"secureBoot"`
		} `json:"hosts"`
	}
	if err := json.Unmarshal([]byte(r.body), &cl); err != nil {
		t.Fatal(err)
	}
	if len(cl.Hosts) != 1 || cl.Hosts[0].MAC != mac || !cl.Hosts[0].SecureBoot {
		t.Fatalf("/cluster hosts must expose secureBoot: %s", r.body)
	}

	r = do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+mac, "")
	if r.status != 200 || strings.Contains(r.body, "shim ") {
		t.Fatalf("plain coreos: %+v", r)
	}
	if hostSecureBoot(t, srv.URL, mac) {
		t.Fatal("a plain /booty.ipxe fetch must clear secureBoot")
	}

	r = do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac=aa:bb:cc:dd:ee:52&sb=1", "")
	if r.status != 200 || !strings.Contains(r.body, "Unknown Host") || strings.Contains(r.body, "Secure Boot") {
		t.Fatalf("unknown hosts keep their menu under sb: %+v", r)
	}
}

func TestIPXESecureBootPerOS(t *testing.T) {
	tests := []struct {
		name      string
		os        string
		trusted   string
		doInstall bool
		want      []string
		absent    []string
	}{
		{"flatcar untrusted", "flatcar", "", false,
			[]string{"Flatcar refused", "echo   CA SHA256: " + flatcarCASha + "\n", "echo   Download:  http://192.168.1.10:8080/boot/secureboot/flatcar-ca.der\n", "choose --timeout ${menu-timeout} --default run-from-disk"},
			[]string{"\nkernel ", "Bluefin"}},
		{"flatcar trusted", "flatcar", "flatcar", false,
			[]string{"kernel http://192.168.1.10:8080/data/flatcar_production_pxe.vmlinuz flatcar.first_boot=1"},
			[]string{"refused", "shim "}},
		{"coreos untrusted", "coreos", "", false,
			[]string{"shim http://192.168.1.10:8080/boot/secureboot/fedora/shimx64.efi || goto shell\nkernel ${BASEURL}/fedora-coreos"},
			[]string{"refused"}},
		{"bluefin untrusted install", "bluefin", "", true,
			[]string{"switch to UEFI HTTP Boot - sb", "/bluefin/aa-bb-cc-dd-ee-53/bluefin-server-netboot.efi"},
			[]string{"\nkernel ", "refused", "CA SHA256"}},
		{"bluefin trusted no install", "bluefin", "flatcar", false,
			[]string{"switch to UEFI HTTP Boot - sb", ":run-from-disk\nexit\n"},
			[]string{"\nkernel ", "refused"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, dir := newTestServer(t)
			setupSecureBoot(t, dir, tc.trusted)
			const mac = "aa:bb:cc:dd:ee:53"
			body := `{"mac":"` + mac + `","hostname":"sb","os":"` + tc.os + `"`
			if tc.doInstall {
				body += `,"doInstall":true`
			}
			register(t, srv.URL, body+"}")

			r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+mac+"&sb=1", "")
			if r.status != 200 || !strings.HasPrefix(r.body, "#!ipxe\n") || strings.Contains(r.body, "[[") {
				t.Fatalf("%+v", r)
			}
			for _, want := range tc.want {
				if !strings.Contains(r.body, want) {
					t.Errorf("missing %q:\n%s", want, r.body)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(r.body, absent) {
					t.Errorf("must not contain %q:\n%s", absent, r.body)
				}
			}
			if !hostSecureBoot(t, srv.URL, mac) {
				t.Error("secureBoot must be recorded")
			}
			h, _ := hardware.Get(mac)
			if h.DoInstall != tc.doInstall {
				t.Errorf("doInstall must survive the refusal untouched: %+v", h)
			}
			if tc.os == "bluefin" && h.InstallServedAt != "" {
				t.Errorf("a refused install must not stamp installServedAt: %+v", h)
			}
		})
	}
}

func TestSecureBootWarnings(t *testing.T) {
	hosts := map[string]*hardware.Host{
		"aa:bb:cc:dd:ee:03": {MAC: "aa:bb:cc:dd:ee:03", OS: "bluefin", SecureBoot: true, DoInstall: true},
		"aa:bb:cc:dd:ee:01": {MAC: "aa:bb:cc:dd:ee:01", OS: "flatcar", SecureBoot: true},
		"aa:bb:cc:dd:ee:02": {MAC: "aa:bb:cc:dd:ee:02", OS: "coreos", SecureBoot: true},
		"aa:bb:cc:dd:ee:04": {MAC: "aa:bb:cc:dd:ee:04", OS: "bluefin", SecureBoot: true},
		"aa:bb:cc:dd:ee:05": {MAC: "aa:bb:cc:dd:ee:05", OS: "flatcar"},
		"aa:bb:cc:dd:ee:06": {MAC: "aa:bb:cc:dd:ee:06", OS: "bluefin", DoInstall: true},
	}
	viper.Set(config.SecureBootTrusted, "")
	t.Cleanup(func() { viper.Set(config.SecureBootTrusted, "") })
	got := secureBootWarnings(hosts)
	want := []string{
		"host aa:bb:cc:dd:ee:01 (flatcar): Secure Boot host; the Flatcar CA is not in --secureBootTrusted, boot refused",
		"host aa:bb:cc:dd:ee:03 (bluefin): reached Booty through the signed iPXE; Bluefin boots its signed netboot UKI only through UEFI HTTP Boot (http://192.168.1.10:8080/bluefin/aa-bb-cc-dd-ee-03/bluefin-server-netboot.efi)",
		"host aa:bb:cc:dd:ee:04 (bluefin): reached Booty through the signed iPXE; Bluefin boots its signed netboot UKI only through UEFI HTTP Boot (http://192.168.1.10:8080/bluefin/aa-bb-cc-dd-ee-04/bluefin-server-netboot.efi)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("untrusted warnings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	viper.Set(config.SecureBootTrusted, "flatcar")
	got = secureBootWarnings(hosts)
	if strings.Join(got, "\n") != strings.Join(want[1:], "\n") {
		t.Fatalf("trusted warnings:\n%s", strings.Join(got, "\n"))
	}
	if got := secureBootWarnings(nil); got == nil || len(got) != 0 {
		t.Fatalf("no hosts must give an empty, non-nil list: %#v", got)
	}
}

func TestInfoAndClusterCarrySecureBootWarnings(t *testing.T) {
	srv, dir := newTestServer(t)
	setupSecureBoot(t, dir, "")
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:61","hostname":"f","os":"flatcar"}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:62","hostname":"b","os":"bluefin","doInstall":true}`)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:63","hostname":"c","os":"coreos"}`)
	for _, mac := range []string{"aa:bb:cc:dd:ee:61", "aa:bb:cc:dd:ee:62", "aa:bb:cc:dd:ee:63"} {
		do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+mac+"&sb=1", "")
	}
	want := []string{
		"host aa:bb:cc:dd:ee:61 (flatcar): Secure Boot host; the Flatcar CA is not in --secureBootTrusted, boot refused",
		"host aa:bb:cc:dd:ee:62 (bluefin): reached Booty through the signed iPXE; Bluefin boots its signed netboot UKI only through UEFI HTTP Boot (http://192.168.1.10:8080/bluefin/aa-bb-cc-dd-ee-62/bluefin-server-netboot.efi)",
	}

	r := do(t, http.MethodGet, srv.URL+"/info", "")
	var info struct {
		SecureBoot struct {
			Enabled  bool     `json:"enabled"`
			Warnings []string `json:"warnings"`
		} `json:"secureBoot"`
	}
	if err := json.Unmarshal([]byte(r.body), &info); err != nil {
		t.Fatal(err)
	}
	if !info.SecureBoot.Enabled || strings.Join(info.SecureBoot.Warnings, "\n") != strings.Join(want, "\n") {
		t.Fatalf("/info.secureBoot: %s", r.body)
	}

	r = do(t, http.MethodGet, srv.URL+"/cluster", "")
	var cl struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(r.body), &cl); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.Join(cl.Warnings, "\n"), strings.Join(want, "\n")) || len(cl.Warnings) != 3 || !strings.Contains(cl.Warnings[0], "unsupported under kubeadm") {
		t.Fatalf("/cluster.warnings must append the Secure Boot ones after the cluster's own: %s", r.body)
	}

	viper.Set(config.SecureBoot, false)
	r = do(t, http.MethodGet, srv.URL+"/info", "")
	if err := json.Unmarshal([]byte(r.body), &info); err != nil {
		t.Fatal(err)
	}
	if info.SecureBoot.Enabled || len(info.SecureBoot.Warnings) != 2 {
		t.Fatalf("warnings must outlive --secureBoot being switched off: %s", r.body)
	}
}
