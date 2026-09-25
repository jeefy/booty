package versions

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/state"
	"github.com/spf13/viper"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func TestMissingFlatcarArtifacts(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  []string
	}{
		{
			name: "complete set of regular files",
			setup: func(t *testing.T, dir string) {
				touch(t, filepath.Join(dir, flatcarKernel))
				touch(t, filepath.Join(dir, flatcarInitrd))
			},
			want: nil,
		},
		{
			name: "complete set behind symlinks",
			setup: func(t *testing.T, dir string) {
				for _, a := range flatcarArtifacts {
					touch(t, filepath.Join(dir, flatcarDir, "1.2.3", a))
					symlink(t, filepath.Join(flatcarDir, "1.2.3", a), filepath.Join(dir, a))
				}
			},
			want: nil,
		},
		{
			name: "kernel missing",
			setup: func(t *testing.T, dir string) {
				touch(t, filepath.Join(dir, flatcarInitrd))
			},
			want: []string{flatcarKernel},
		},
		{
			name: "dangling symlink counts as missing",
			setup: func(t *testing.T, dir string) {
				touch(t, filepath.Join(dir, flatcarKernel))
				symlink(t, filepath.Join(flatcarDir, "gone", flatcarInitrd), filepath.Join(dir, flatcarInitrd))
			},
			want: []string{flatcarInitrd},
		},
		{
			name:  "empty dir",
			setup: func(*testing.T, string) {},
			want:  []string{flatcarKernel, flatcarInitrd},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			if got := MissingFlatcarArtifacts(dir); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestMissingCoreOSArtifacts(t *testing.T) {
	const ver, arch = "44.20260913.2.1", "x86_64"
	names := coreOSArtifactNames(ver, arch)

	dir := t.TempDir()
	touch(t, filepath.Join(dir, names["initramfs"]))
	touch(t, filepath.Join(dir, names["rootfs"]))
	if got := MissingCoreOSArtifacts(dir, ver, arch); !reflect.DeepEqual(got, []string{names["kernel"]}) {
		t.Fatalf("got %v want only kernel", got)
	}

	touch(t, filepath.Join(dir, names["kernel"]))
	if got := MissingCoreOSArtifacts(dir, ver, arch); got != nil {
		t.Fatalf("complete set reported missing: %v", got)
	}
	if got := MissingCoreOSArtifacts(dir, "1.0.0", arch); len(got) != 3 {
		t.Fatalf("other version should miss all three, got %v", got)
	}
}

func setupVerify(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.DataDir, dir)
	viper.Set(config.CoreOSChannel, "stable")
	viper.Set(config.CoreOSArchitecture, "x86_64")
	config.LoadConfig()
	state.SetCurrentFlatcarVersion("")
	state.SetCurrentCoreOSVersion("")
	t.Cleanup(func() {
		state.SetCurrentFlatcarVersion("")
		state.SetCurrentCoreOSVersion("")
	})
	return dir
}

func TestVerifyLocalArtifacts(t *testing.T) {
	const coreosVer = "39.20231101.3.0"
	coreosNames := coreOSArtifactNames(coreosVer, "x86_64")
	writeStreams := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "stable.json"), []byte(streamsFixture), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("flatcar incomplete resets to 0.0.0", func(t *testing.T) {
		dir := setupVerify(t)
		state.SetCurrentFlatcarVersion("9.9.9")
		touch(t, filepath.Join(dir, flatcarKernel))
		VerifyLocalArtifacts()
		if got := state.CurrentFlatcarVersion(); got != "0.0.0" {
			t.Fatalf("flatcar version %q want 0.0.0", got)
		}
		if _, err := os.Stat(filepath.Join(dir, flatcarKernel)); err != nil {
			t.Fatal("flatcar files must not be deleted:", err)
		}
	})

	t.Run("flatcar complete is untouched", func(t *testing.T) {
		dir := setupVerify(t)
		state.SetCurrentFlatcarVersion("9.9.9")
		touch(t, filepath.Join(dir, flatcarKernel))
		touch(t, filepath.Join(dir, flatcarInitrd))
		VerifyLocalArtifacts()
		if got := state.CurrentFlatcarVersion(); got != "9.9.9" {
			t.Fatalf("flatcar version %q want 9.9.9", got)
		}
	})

	t.Run("unset versions are a no-op", func(t *testing.T) {
		setupVerify(t)
		for _, v := range []string{"", "0.0.0"} {
			state.SetCurrentFlatcarVersion(v)
			VerifyLocalArtifacts()
			if got := state.CurrentFlatcarVersion(); got != v {
				t.Fatalf("flatcar version %q changed to %q", v, got)
			}
		}
		if got := state.CurrentCoreOSVersion(); got != "0.0.0" {
			t.Fatalf("coreos without streams JSON should load as 0.0.0, got %q", got)
		}
	})

	t.Run("coreos partial set is deleted and reset", func(t *testing.T) {
		dir := setupVerify(t)
		writeStreams(t, dir)
		touch(t, filepath.Join(dir, coreosNames["initramfs"]))
		touch(t, filepath.Join(dir, coreosNames["rootfs"]))
		VerifyLocalArtifacts()
		if got := state.CurrentCoreOSVersion(); got != "0.0.0" {
			t.Fatalf("coreos version %q want 0.0.0", got)
		}
		for _, f := range coreosNames {
			if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
				t.Fatalf("%s should have been removed (err=%v)", f, err)
			}
		}
	})

	t.Run("coreos complete set is kept", func(t *testing.T) {
		dir := setupVerify(t)
		writeStreams(t, dir)
		for _, f := range coreosNames {
			touch(t, filepath.Join(dir, f))
		}
		VerifyLocalArtifacts()
		if got := state.CurrentCoreOSVersion(); got != coreosVer {
			t.Fatalf("coreos version %q want %s", got, coreosVer)
		}
		for _, f := range coreosNames {
			if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
				t.Fatalf("%s should still exist: %v", f, err)
			}
		}
	})
}
