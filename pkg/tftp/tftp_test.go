package tftp

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

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
		r, err := openRequest(name)
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
		r, err := openRequest(name)
		if err != nil {
			t.Errorf("%q should be served: %v", name, err)
			continue
		}
		if got := readAll(t, r); got != want {
			t.Errorf("%q: got %q want %q", name, got, want)
		}
	}
}

func TestOpenRequestServesEmbeddedBootFiles(t *testing.T) {
	dir := setupDataDir(t)
	if err := os.WriteFile(filepath.Join(dir, "ipxe.efi"), []byte("STALE ON DISK"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg = Config{BootFiles: fstest.MapFS{
		"undionly.kpxe": {Data: []byte("BIOS")},
		"ipxe.efi":      {Data: []byte("EFI")},
		"snponly.efi":   {Data: []byte("SNP")},
	}}
	t.Cleanup(func() { cfg = Config{} })

	for name, want := range map[string]string{
		"undionly.kpxe": "BIOS",
		"ipxe.efi":      "EFI",
		"snponly.efi":   "SNP",
	} {
		r, err := openRequest(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := readAll(t, r); got != want {
			t.Errorf("%s: got %q want %q", name, got, want)
		}
	}
}

func TestOpenRequestBootFilesFallBackToDataDir(t *testing.T) {
	dir := setupDataDir(t)
	if err := os.WriteFile(filepath.Join(dir, "undionly.kpxe"), []byte("FROM DISK"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg = Config{}

	r, err := openRequest("undionly.kpxe")
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, r); got != "FROM DISK" {
		t.Fatalf("got %q", got)
	}
	if _, err := openRequest("ipxe.efi"); err == nil {
		t.Fatal("ipxe.efi should be not found without embedded files or a copy in DataDir")
	}
}

func TestOpenRequestNoPxelinux(t *testing.T) {
	setupDataDir(t)
	for _, name := range []string{
		"pxelinux.cfg/default",
		"pxelinux.cfg/01-aa-bb-cc-dd-ee-ff",
		"pxelinux.0",
		"ldlinux.c32",
	} {
		if r, err := openRequest(name); err == nil {
			readAll(t, r)
			t.Errorf("%q should be a file-not-found: pxelinux support was removed", name)
		}
	}
}

func TestOpenRequestBootyIPXEStub(t *testing.T) {
	setupDataDir(t)
	r, err := openRequest("booty.ipxe")
	if err != nil {
		t.Fatal(err)
	}
	got := readAll(t, r)
	want := "#!ipxe\nchain http://192.168.1.10:8080/booty.ipxe?mac=${mac}\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
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
		if !strings.HasSuffix(key, ".ipxe") {
			t.Errorf("template %q is not an iPXE script; pxelinux configs were removed", key)
		}
		if !strings.HasPrefix(tmpl, "#!ipxe\n") {
			t.Errorf("template %q must start with #!ipxe", key)
		}
		for _, line := range strings.Split(tmpl, "\n") {
			if strings.HasPrefix(line, "\t") {
				t.Errorf("template %q has a leading tab line %q", key, line)
			}
		}
	}
}
