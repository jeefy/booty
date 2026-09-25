package tftp

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

func setupDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	viper.Set(config.DataDir, dir)
	viper.Set(config.HardwareMap, "hardware.json")
	viper.Set(config.ServerIP, "192.168.1.10")
	viper.Set(config.ServerHttpPort, 8080)
	if err := hardware.Load(); err != nil {
		t.Fatalf("hardware.Load: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plain.txt"), []byte("plain"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "inner.txt"), []byte("inner"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readAll(t *testing.T, r io.ReadCloser) string {
	t.Helper()
	defer func() {
		if err := r.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

func TestOpenRequestRejectsTraversal(t *testing.T) {
	dir := setupDataDir(t)
	outside := filepath.Join(filepath.Dir(dir), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("plain.txt", filepath.Join(dir, "inside")); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{
		"../outside.txt",
		"/etc/passwd",
		"sub/../../outside.txt",
		"a/../../x",
		"plain.txt\x00.ipxe",
		"escape",
		"sub",
		"missing.bin",
	} {
		r, err := openRequest(name, nil)
		if err == nil {
			readAll(t, r)
			t.Errorf("%q should have been rejected", name)
		}
	}

	for name, want := range map[string]string{
		"plain.txt":     "plain",
		"sub/inner.txt": "inner",
		"inside":        "plain",
	} {
		r, err := openRequest(name, nil)
		if err != nil {
			t.Errorf("%q should be served: %v", name, err)
			continue
		}
		if got := readAll(t, r); got != want {
			t.Errorf("%q: got %q want %q", name, got, want)
		}
	}
}

func TestOpenRequestServesEmbeddedUndionly(t *testing.T) {
	setupDataDir(t)
	cfg = Config{UndionlyKPXE: []byte("EMBEDDED")}
	t.Cleanup(func() { cfg = Config{} })

	r, err := openRequest("undionly.kpxe", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, r); got != "EMBEDDED" {
		t.Fatalf("got %q", got)
	}
}

func TestOpenRequestBootyIPXEStub(t *testing.T) {
	setupDataDir(t)
	r, err := openRequest("booty.ipxe", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := readAll(t, r)
	want := "#!ipxe\nchain http://192.168.1.10:8080/booty.ipxe?mac=${mac}\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestParsePXELinuxMAC(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"pxelinux.cfg/01-aa-bb-cc-dd-ee-ff", "aa:bb:cc:dd:ee:ff", true},
		{"pxelinux.cfg/01-AA-BB-CC-DD-EE-FF", "aa:bb:cc:dd:ee:ff", true},
		{"pxelinux.cfg/01-aa-bb-cc-dd-ee", "", false},
		{"pxelinux.cfg/01-zz-bb-cc-dd-ee-ff", "", false},
		{"pxelinux.cfg/default", "", false},
		{"pxelinux.cfg/C0A8000A", "", false},
		{"01-aa-bb-cc-dd-ee-ff", "", false},
	}
	for _, tc := range tests {
		got, ok := ParsePXELinuxMAC(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParsePXELinuxMAC(%q)=(%q,%v) want (%q,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestLegacyPXEByMAC(t *testing.T) {
	setupDataDir(t)
	if _, err := hardware.Put(hardware.Host{MAC: "aa:bb:cc:dd:ee:01", OS: "ublue"}); err != nil {
		t.Fatal(err)
	}

	r, err := openRequest("pxelinux.cfg/01-aa-bb-cc-dd-ee-01", net.ParseIP("10.0.0.5"))
	if err != nil {
		t.Fatal(err)
	}
	got := readAll(t, r)
	if !strings.Contains(got, "ignition.config.url=http://192.168.1.10:8080/ignition.json") {
		t.Fatalf("registered ublue host should fall back to flatcar legacy config, got:\n%s", got)
	}
	if strings.Contains(got, "[[") {
		t.Fatalf("unsubstituted placeholder:\n%s", got)
	}

	r, err = openRequest("pxelinux.cfg/01-aa-bb-cc-dd-ee-02", net.ParseIP("10.0.0.6"))
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, r); !strings.Contains(got, "localboot 0") {
		t.Fatalf("unknown host should get local boot config, got:\n%s", got)
	}
	unknown := hardware.Snapshot().UnknownHosts["aa:bb:cc:dd:ee:02"]
	if unknown == nil || unknown.IP != "10.0.0.6" {
		t.Fatalf("unknown host should have been observed, got %+v", unknown)
	}
}

func TestLegacyPXEDefaultUsesARP(t *testing.T) {
	setupDataDir(t)
	if _, err := hardware.Put(hardware.Host{MAC: "aa:bb:cc:dd:ee:01", OS: "flatcar"}); err != nil {
		t.Fatal(err)
	}
	orig := arpLookup
	t.Cleanup(func() { arpLookup = orig })
	arpLookup = func(ip net.IP) (net.HardwareAddr, error) {
		return net.HardwareAddr{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0x01}, nil
	}

	r, err := openRequest("pxelinux.cfg/default", net.ParseIP("10.0.0.5"))
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, r); !strings.Contains(got, "label flatcar") {
		t.Fatalf("expected flatcar config, got:\n%s", got)
	}

	r, err = openRequest("pxelinux.cfg/default", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, r); !strings.Contains(got, "localboot 0") {
		t.Fatalf("nil remote IP must not call arping and should serve unknown config, got:\n%s", got)
	}
}

func TestIPXEScriptRendering(t *testing.T) {
	vars := TemplateVars{
		Server:        "192.168.1.10:8080",
		CoreOSChannel: "stable",
		CoreOSArch:    "x86_64",
		CoreOSVersion: "39.20231101.3.0",
		OSTreeImage:   "ghcr.io/ublue-os/bazzite:stable",
	}

	flatcar := &hardware.Host{MAC: "aa:bb:cc:dd:ee:01", OS: "flatcar"}
	vars.MenuDefault = MenuDefaultForHost(flatcar)
	out := IPXEScript(OSForHost(flatcar), vars)
	if !strings.HasPrefix(out, "#!ipxe\n") || !strings.Contains(out, "kernel http://192.168.1.10:8080/data/flatcar_production_pxe.vmlinuz") {
		t.Fatalf("flatcar script wrong:\n%s", out)
	}
	if !strings.Contains(out, "ignition.config.url=http://192.168.1.10:8080/ignition.json?mac=${mac}") {
		t.Fatalf("flatcar script must pass the client MAC to the ignition fetch:\n%s", out)
	}
	if strings.Contains(out, "[[") || strings.Contains(out, "\n\t") {
		t.Fatalf("flatcar script has placeholders or leading tabs:\n%s", out)
	}

	ublue := &hardware.Host{MAC: "aa:bb:cc:dd:ee:02", OS: "ublue", DoInstall: true, OSTreeImage: vars.OSTreeImage}
	vars.MenuDefault = MenuDefaultForHost(ublue)
	out = IPXEScript(OSForHost(ublue), vars)
	for _, want := range []string{
		"set menu-default install",
		"set OSTREE_IMAGE ghcr.io/ublue-os/bazzite:stable",
		"set CONFIGURL http://192.168.1.10:8080/ignition.json?mac=${mac}",
		"set VERSION 39.20231101.3.0",
		"chain http://192.168.1.10:8080/data/ublue.ipxe",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ublue script missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "[[") {
		t.Fatalf("ublue script has placeholders:\n%s", out)
	}

	vars.MenuDefault = MenuDefaultForHost(nil)
	out = IPXEScript(OSForHost(nil), vars)
	if !strings.Contains(out, "Unknown Host") || !strings.Contains(out, "${mac}") || strings.Contains(out, "[[") {
		t.Fatalf("unknown script wrong:\n%s", out)
	}

	weird := &hardware.Host{MAC: "aa:bb:cc:dd:ee:03", OS: "templeos"}
	if got := OSForHost(weird); got != DefaultOS {
		t.Fatalf("unknown OS should fall back to %q, got %q", DefaultOS, got)
	}

	for key, tmpl := range PXEConfig {
		for _, line := range strings.Split(tmpl, "\n") {
			if strings.HasPrefix(line, "\t") && strings.HasSuffix(key, ".ipxe") {
				t.Errorf("template %q has a leading tab line %q", key, line)
			}
		}
	}
}
