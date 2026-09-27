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

	coreos := &hardware.Host{MAC: "aa:bb:cc:dd:ee:02", OS: "coreos", DoInstall: true, OSTreeImage: vars.OSTreeImage}
	vars.MenuDefault = MenuDefaultForHost(coreos)
	out = IPXEScript(OSForHost(coreos), vars)
	for _, want := range []string{
		"set OSTREE_IMAGE ghcr.io/ublue-os/bazzite:stable",
		"set CONFIGURL http://192.168.1.10:8080/ignition.json?mac=${mac}",
		"set VERSION 39.20231101.3.0",
		"kernel ${BASEURL}/fedora-coreos-${VERSION}-live-kernel-${ARCH}",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("coreos script missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "[[") {
		t.Fatalf("coreos script has placeholders:\n%s", out)
	}

	if _, stale := PXEConfig["ublue.ipxe"]; stale {
		t.Fatal("ublue was replaced by bluefin")
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

func TestBluefinScriptRendering(t *testing.T) {
	const url = "http://192.168.1.10:8080/bluefin/aa-bb-cc-dd-ee-03/bluefin-server-netboot.efi"
	host := &hardware.Host{MAC: "aa:bb:cc:dd:ee:03", OS: "bluefin", DoInstall: true, InstallDisk: "/dev/vda"}
	if got := OSForHost(host); got != "bluefin" {
		t.Fatalf("OSForHost=%q", got)
	}
	for _, sb := range []bool{false, true} {
		out := IPXEScript("bluefin", TemplateVars{Server: "192.168.1.10:8080", Hostname: "srv1", BluefinBootURL: url, SecureBoot: sb})
		for _, want := range []string{
			"#!ipxe\niseq ${platform} efi || goto not-efi\n",
			"echo Switch this machine's network boot to UEFI HTTP Boot (IPv4)",
			"echo   " + url + "\n",
			"menu Booty - Bluefin Server: switch to UEFI HTTP Boot - srv1\n",
			"item --key d run-from-disk Boot from disk\n",
			"item --key s shell         iPXE shell\n",
			"choose --timeout ${menu-timeout} --default run-from-disk selected || goto run-from-disk\n",
			":run-from-disk\nexit\n",
			":not-efi\necho Bluefin Server needs UEFI",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("sb=%v: bluefin script missing %q:\n%s", sb, want, out)
			}
		}
		for _, absent := range []string{"[[", "\nkernel ", "\ninitrd ", "install", "inst."} {
			if strings.Contains(out, absent) {
				t.Errorf("sb=%v: bluefin script must not contain %q:\n%s", sb, absent, out)
			}
		}
	}
}

func TestSecureBootScriptVariants(t *testing.T) {
	base := TemplateVars{
		Server:          "192.168.1.10:8080",
		Hostname:        "sb1",
		CoreOSChannel:   "stable",
		CoreOSArch:      "x86_64",
		CoreOSVersion:   "43.20260901.3.0",
		FlatcarCASha256: "ebb170da86aa56bae7abd15214c6ee48171d4bde8bc437400e16752c4925dba2",
		BluefinBootURL:  "http://192.168.1.10:8080/bluefin/aa-bb-cc-dd-ee-53/bluefin-server-netboot.efi",
	}
	const (
		shimLine   = "shim http://192.168.1.10:8080/boot/secureboot/fedora/shimx64.efi || goto shell\n"
		caLine     = "echo   CA SHA256: ebb170da86aa56bae7abd15214c6ee48171d4bde8bc437400e16752c4925dba2\n"
		caURL      = "echo   Download:  http://192.168.1.10:8080/boot/secureboot/flatcar-ca.der\n"
		refuseMenu = "menu Booty - Secure Boot: "
	)

	tests := []struct {
		name    string
		os      string
		sb      bool
		trusted bool
		refused bool
		want    []string
		absent  []string
	}{
		{"coreos plain", "coreos", false, false, false, []string{"kernel ${BASEURL}/fedora-coreos"}, []string{"shim ", refuseMenu}},
		{"coreos sb", "coreos", true, false, false, []string{shimLine + "kernel ${BASEURL}/fedora-coreos"}, []string{refuseMenu}},
		{"coreos sb trusted", "coreos", true, true, false, []string{shimLine}, []string{refuseMenu}},
		{"flatcar plain", "flatcar", false, false, false, []string{"kernel http://192.168.1.10:8080/data/flatcar_production_pxe.vmlinuz"}, []string{"shim ", refuseMenu}},
		{"flatcar sb untrusted", "flatcar", true, false, true,
			[]string{
				"echo Booty: this machine reached Booty through Secure Boot, and Flatcar cannot boot that way.\n",
				"--secureBootTrusted=flatcar", caLine, caURL,
				"menu Booty - Secure Boot: Flatcar refused - sb1\n",
				"item --key d run-from-disk Boot from disk\n", "item --key r reboot        Reboot\n", "item --key s shell         iPXE shell\n",
				"choose --timeout ${menu-timeout} --default run-from-disk selected || goto run-from-disk\n",
				"set menu-timeout 30000\n", ":run-from-disk\nexit\n", ":reboot\nreboot\n",
			},
			[]string{"\nkernel ", "\ninitrd ", "Bluefin"}},
		{"flatcar sb trusted", "flatcar", true, true, false, []string{"kernel http://192.168.1.10:8080/data/flatcar_production_pxe.vmlinuz"}, []string{"shim ", refuseMenu}},
		{"bluefin plain", "bluefin", false, false, false, []string{"switch to UEFI HTTP Boot - sb1"}, []string{refuseMenu, "\nkernel "}},
		{"bluefin sb untrusted", "bluefin", true, false, false,
			[]string{"switch to UEFI HTTP Boot - sb1", "/bluefin/aa-bb-cc-dd-ee-53/bluefin-server-netboot.efi"},
			[]string{refuseMenu, "\nkernel ", "CA SHA256"}},
		{"bluefin sb trusted", "bluefin", true, true, false,
			[]string{"switch to UEFI HTTP Boot - sb1"},
			[]string{refuseMenu, "\nkernel ", "CA SHA256"}},
		{"unknown sb", "unknown", true, false, false, []string{"Unknown Host"}, []string{refuseMenu, "shim "}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := base
			v.SecureBoot, v.SecureBootTrustedFlatcar = tc.sb, tc.trusted
			v.MenuDefault = "install"
			if got := SecureBootRefused(tc.os, v); got != tc.refused {
				t.Fatalf("SecureBootRefused=%v want %v", got, tc.refused)
			}
			out := IPXEScript(tc.os, v)
			if !strings.HasPrefix(out, "#!ipxe\n") || strings.Contains(out, "[[") {
				t.Fatalf("bad script:\n%s", out)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q:\n%s", want, out)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(out, absent) {
					t.Errorf("must not contain %q:\n%s", absent, out)
				}
			}
			if tc.refused {
				for _, line := range strings.Split(out, "\n") {
					if strings.Contains(line, "${") && !strings.Contains(line, "${menu-timeout}") && !strings.Contains(line, "${selected}") {
						t.Errorf("refusal menu must not expand iPXE variables in prose: %q", line)
					}
				}
			}
		})
	}

	v := base
	v.SecureBoot, v.FlatcarCASha256 = true, ""
	if out := IPXEScript("flatcar", v); !strings.Contains(out, "echo   CA SHA256: (not extracted yet: Booty has not synced a Flatcar release)\n") {
		t.Fatalf("missing fingerprint must be explained:\n%s", out)
	}
}

func TestNonSecureBootRenderingUnchanged(t *testing.T) {
	v := TemplateVars{Server: "192.168.1.10:8080", Hostname: "h", MenuDefault: "run-from-disk", CoreOSChannel: "stable", CoreOSArch: "x86_64", CoreOSVersion: "1"}
	for _, os := range []string{"flatcar", "coreos", "bluefin", "unknown"} {
		plain := IPXEScript(os, v)
		withTrust := v
		withTrust.SecureBootTrustedFlatcar, withTrust.FlatcarCASha256 = true, "ff"
		if got := IPXEScript(os, withTrust); got != plain {
			t.Errorf("%s: SecureBootTrustedFlatcar/FlatcarCASha256 must not change a non-sb render", os)
		}
		if strings.Contains(plain, "shim ") || strings.Contains(plain, "Secure Boot") {
			t.Errorf("%s: non-sb render mentions Secure Boot:\n%s", os, plain)
		}
	}
}
