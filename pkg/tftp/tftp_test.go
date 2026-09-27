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
	vars := TemplateVars{
		Server:   "192.168.1.10:8080",
		Hostname: "srv1",
		Bluefin: BluefinVars{
			Version:     "26.08.0",
			Vmlinuz:     "bluefin-server-pxe-vmlinuz-26.08.0",
			Initrd:      "bluefin-server-pxe-initrd-26.08.0.cpio.gz",
			DDI:         "bluefin-server-ddi-4593.2.5.raw.zst",
			DDISha256:   strings.Repeat("a", 64),
			InstallDisk: "/dev/vda",
			CredsURL:    "http://192.168.1.10:8080/creds/aa:bb:cc:dd:ee:03.tar",
			CredsSha256: strings.Repeat("b", 64),
		},
	}
	host := &hardware.Host{MAC: "aa:bb:cc:dd:ee:03", OS: "bluefin", DoInstall: true, InstallDisk: "/dev/vda"}
	vars.MenuDefault = MenuDefaultForHost(host)
	if got := OSForHost(host); got != "bluefin" {
		t.Fatalf("OSForHost=%q", got)
	}
	out := IPXEScript(OSForHost(host), vars)
	for _, want := range []string{
		"#!ipxe\niseq ${platform} efi || goto not-efi\n",
		"set BASEURL http://192.168.1.10:8080/data/bluefin/current\n",
		"menu Booty - Bluefin Server 26.08.0 - srv1\n",
		"Install Bluefin Server to /dev/vda (wipes it)\n",
		"choose --timeout ${menu-timeout} --default install selected || goto run-from-disk\n",
		"kernel ${BASEURL}/bluefin-server-pxe-vmlinuz-26.08.0 systemd.unit=system-install.target console=tty0 console=ttyS0,115200 rw unattended inst.ddi_url=${BASEURL}/bluefin-server-ddi-4593.2.5.raw.zst inst.ddi_sha256=" + strings.Repeat("a", 64) + " inst.target_disk=/dev/vda inst.creds_url=http://192.168.1.10:8080/creds/aa:bb:cc:dd:ee:03.tar inst.creds_sha256=" + strings.Repeat("b", 64) + "\n",
		"initrd ${BASEURL}/bluefin-server-pxe-initrd-26.08.0.cpio.gz\n",
		":run-from-disk\nexit\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("bluefin script missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "[[") {
		t.Fatalf("bluefin script has placeholders:\n%s", out)
	}

	vars.Bluefin.InstallDisk, vars.Bluefin.CredsURL = "", ""
	host.DoInstall = false
	vars.MenuDefault = MenuDefaultForHost(host)
	out = IPXEScript("bluefin", vars)
	if strings.Contains(out, "inst.target_disk") || strings.Contains(out, "inst.creds_") || !strings.Contains(out, "to the first writable disk (wipes it)") || !strings.Contains(out, "--default run-from-disk") {
		t.Fatalf("optional args must vanish without disk/creds:\n%s", out)
	}
	if !strings.Contains(out, "inst.ddi_sha256="+strings.Repeat("a", 64)) {
		t.Fatalf("ddi sha is mandatory:\n%s", out)
	}

	out = IPXEScript("bluefin", TemplateVars{Server: "192.168.1.10:8080", Hostname: "srv1", MenuDefault: "install"})
	if !strings.Contains(out, "echo Bluefin artifacts not downloaded yet") || strings.Contains(out, "kernel ") || strings.Contains(out, "item --key i install") || strings.Contains(out, "[[") {
		t.Fatalf("without a cached release only run-from-disk/shell are offered:\n%s", out)
	}
	if !strings.Contains(out, ":run-from-disk\nexit\n") || !strings.Contains(out, "srv1") {
		t.Fatalf("pending menu wrong:\n%s", out)
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
		Bluefin: BluefinVars{
			Version: "26.08.0", Vmlinuz: "bluefin-server-pxe-vmlinuz-26.08.0",
			Initrd: "bluefin-server-pxe-initrd-26.08.0.cpio.gz", DDI: "ddi.raw.zst", DDISha256: strings.Repeat("a", 64),
		},
	}
	const (
		shimLine   = "shim http://192.168.1.10:8080/boot/secureboot/fedora/shimx64.efi || goto shell\n"
		caLine     = "echo   CA SHA256: ebb170da86aa56bae7abd15214c6ee48171d4bde8bc437400e16752c4925dba2\n"
		caURL      = "echo   Download:  http://192.168.1.10:8080/boot/secureboot/flatcar-ca.der\n"
		unsigned   = "echo Bluefin Server's installed system (systemd-boot and its UKI) is not signed"
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
			[]string{"\nkernel ", "\ninitrd ", unsigned}},
		{"flatcar sb trusted", "flatcar", true, true, false, []string{"kernel http://192.168.1.10:8080/data/flatcar_production_pxe.vmlinuz"}, []string{"shim ", refuseMenu}},
		{"bluefin plain", "bluefin", false, false, false, []string{"item --key i install"}, []string{refuseMenu}},
		{"bluefin sb untrusted", "bluefin", true, false, true,
			[]string{"Bluefin Server cannot boot that way", caLine, caURL, unsigned, "menu Booty - Secure Boot: Bluefin Server refused - sb1\n"},
			[]string{"\nkernel ", "item --key i install", "inst.ddi_url"}},
		{"bluefin sb trusted", "bluefin", true, true, true,
			[]string{unsigned, "menu Booty - Secure Boot: Bluefin Server refused - sb1\n"},
			[]string{"\nkernel ", "CA SHA256", "item --key i install"}},
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
