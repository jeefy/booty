package state

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func setup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, dir)
	config.LoadConfig()
	s = runtimeState{}
	t.Cleanup(func() { s = runtimeState{} })
	return dir
}

func TestInitReadsLocalVersion(t *testing.T) {
	dir := setup(t)
	if err := os.WriteFile(filepath.Join(dir, "version.txt"), []byte("FLATCAR_BUILD=3815\nFLATCAR_VERSION=3815.2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	Init()
	if got := CurrentFlatcarVersion(); got != "3815.2.0" {
		t.Fatalf("CurrentFlatcarVersion=%q want 3815.2.0", got)
	}
	if FlatcarPin() != "" {
		t.Fatalf("pin should be empty, got %q", FlatcarPin())
	}
}

func TestInitWithoutVersionFile(t *testing.T) {
	setup(t)
	Init()
	if CurrentFlatcarVersion() != "" || RemoteFlatcarVersion() != "" {
		t.Fatalf("versions should be empty: %q %q", CurrentFlatcarVersion(), RemoteFlatcarVersion())
	}
}

func TestPinPrecedence(t *testing.T) {
	writePinFile := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, config.FlatcarPinFile), []byte("1.1.1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("file only", func(t *testing.T) {
		dir := setup(t)
		writePinFile(t, dir)
		Init()
		if FlatcarPin() != "1.1.1" {
			t.Fatalf("pin=%q want 1.1.1", FlatcarPin())
		}
	})

	t.Run("env beats file", func(t *testing.T) {
		dir := setup(t)
		writePinFile(t, dir)
		t.Setenv("FLATCAR_VERSION_PIN", "2.2.2")
		Init()
		if FlatcarPin() != "2.2.2" {
			t.Fatalf("pin=%q want 2.2.2", FlatcarPin())
		}
	})

	t.Run("flag beats env and file", func(t *testing.T) {
		dir := setup(t)
		writePinFile(t, dir)
		t.Setenv("FLATCAR_VERSION_PIN", "2.2.2")
		viper.Set(config.FlatcarVersion, "3.3.3")
		Init()
		if FlatcarPin() != "3.3.3" {
			t.Fatalf("pin=%q want 3.3.3", FlatcarPin())
		}
	})

	t.Run("BOOTY_ prefixed env", func(t *testing.T) {
		setup(t)
		t.Setenv("BOOTY_FLATCARVERSION", "4.4.4")
		Init()
		if FlatcarPin() != "4.4.4" {
			t.Fatalf("pin=%q want 4.4.4", FlatcarPin())
		}
	})
}

func TestSetFlatcarPinPersistsAndClears(t *testing.T) {
	dir := setup(t)
	pinPath := filepath.Join(dir, config.FlatcarPinFile)

	if err := SetFlatcarPin("  3815.2.0 \n"); err != nil {
		t.Fatalf("SetFlatcarPin: %v", err)
	}
	if FlatcarPin() != "3815.2.0" {
		t.Fatalf("pin=%q", FlatcarPin())
	}
	data, err := os.ReadFile(pinPath)
	if err != nil || string(data) != "3815.2.0\n" {
		t.Fatalf("pin file content %q err=%v", data, err)
	}

	s = runtimeState{}
	Init()
	if FlatcarPin() != "3815.2.0" {
		t.Fatalf("pin should survive restart, got %q", FlatcarPin())
	}

	if err := SetFlatcarPin(""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if FlatcarPin() != "" {
		t.Fatalf("pin should be cleared, got %q", FlatcarPin())
	}
	if _, err := os.Stat(pinPath); !os.IsNotExist(err) {
		t.Fatalf("pin file should be removed, stat err=%v", err)
	}
	if err := SetFlatcarPin(""); err != nil {
		t.Fatalf("clearing twice must be idempotent: %v", err)
	}
}

func TestInitReadsBluefinManifestAndPin(t *testing.T) {
	dir := setup(t)
	Init()
	if CurrentBluefinVersion() != "" || BluefinPin() != "" {
		t.Fatalf("no manifest: version=%q pin=%q", CurrentBluefinVersion(), BluefinPin())
	}

	rel := filepath.Join(dir, "bluefin", "26.08.0")
	if err := os.MkdirAll(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rel, "manifest.json"), []byte(`{"version":"26.08.0","vmlinuz":"k"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("26.08.0", filepath.Join(dir, "bluefin", "current")); err != nil {
		t.Fatal(err)
	}
	viper.Set(config.BluefinVersion, " 26.08.0 ")
	s = runtimeState{}
	Init()
	if got := CurrentBluefinVersion(); got != "26.08.0" {
		t.Fatalf("CurrentBluefinVersion=%q", got)
	}
	if got := BluefinPin(); got != "26.08.0" {
		t.Fatalf("BluefinPin=%q", got)
	}

	if err := os.WriteFile(filepath.Join(rel, "manifest.json"), []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LoadLocalBluefinVersion(); got != "" {
		t.Fatalf("unparseable manifest should yield \"\", got %q", got)
	}
}
