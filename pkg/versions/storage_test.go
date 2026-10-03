package versions

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/state"
	"github.com/spf13/viper"
)

func writeBytes(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentStorage(t *testing.T) {
	dir := homelabLayout(t)
	MigrateReleaseLayout()
	InvalidateStorage()
	for _, v := range []string{"27.01.1", "27.02.2"} {
		writeTestBluefinRelease(t, dir, v)
		writeBytes(t, filepath.Join(dir, "bluefin", v, BluefinDDI(v)), 1000)
		if err := linkRelease(OSBluefin, v); err != nil {
			t.Fatal(err)
		}
	}
	writeBytes(t, filepath.Join(dir, "flatcar", homelabFlatcar, flatcarKernel), 300)
	writeBytes(t, filepath.Join(dir, "ipxe.efi"), 50)
	writeBytes(t, filepath.Join(dir, "ipxe-shimx64.efi"), 20)
	writeBytes(t, filepath.Join(dir, "pxelinux.0"), 7)
	writeBytes(t, filepath.Join(dir, "grubx64.efi"), 8)
	writeBytes(t, filepath.Join(dir, "autopilot", "state.json"), 11)
	writeBytes(t, filepath.Join(dir, "autopilot", "reports", "bluefin-27.01.1.md"), 13)
	writeBytes(t, filepath.Join(dir, "secureboot", "v1", "shimx64.efi"), 17)
	for _, h := range []hardware.Host{
		{MAC: "aa:bb:cc:dd:ee:01", OS: "bluefin", Running: "27.02.2"},
		{MAC: "aa:bb:cc:dd:ee:02", OS: "bluefin", Running: "27.01.1", TargetVersion: "27.01.1"},
		{MAC: "aa:bb:cc:dd:ee:03", OS: "flatcar", Running: "27.02.2"},
		{MAC: "aa:bb:cc:dd:ee:04", OS: "bluefin", TargetVersion: homelabBluefinPr},
	} {
		if _, err := hardware.Put(h); err != nil {
			t.Fatal(err)
		}
	}
	viper.Set(config.FlatcarChannel, "beta")
	if err := state.SetFlatcarPin(homelabFlatcar); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.SetFlatcarPin("") })

	// Each test release is the 1000-byte DDI plus manifest.json and three
	// 1-byte touched files.
	manifestBytes := func(v string) int64 {
		info, err := os.Stat(filepath.Join(dir, "bluefin", v, config.BluefinManifestFile))
		if err != nil {
			t.Fatal(err)
		}
		return info.Size() + 3
	}
	s := CurrentStorage()
	if s.DataDir != dir || s.Total.Bytes <= 0 || s.Free.Bytes <= 0 || s.Free.Bytes > s.Total.Bytes {
		t.Fatalf("statfs: %+v", s)
	}
	if time.Since(s.ComputedAt) > time.Minute {
		t.Fatalf("computedAt=%s", s.ComputedAt)
	}

	bf := s.OS[OSBluefin]
	if !bf.Tracked || bf.Source != config.DefaultBluefinRepo || bf.Pin != "" {
		t.Fatalf("bluefin block: %+v", bf)
	}
	versions := make([]string, 0, len(bf.Releases))
	for _, r := range bf.Releases {
		versions = append(versions, r.Version)
	}
	if want := []string{homelabBluefinPr, "27.02.2", "27.01.1", homelabBluefin}; !reflect.DeepEqual(versions, want) {
		t.Fatalf("releases newest first: %v want %v", versions, want)
	}
	byVersion := map[string]StorageRelease{}
	for _, r := range bf.Releases {
		byVersion[r.Version] = r
	}
	cur := byVersion["27.02.2"]
	if cur.Bytes != 1000+manifestBytes("27.02.2") || cur.Files != 5 || cur.Retained != RetainedCurrent || !cur.Cached || !reflect.DeepEqual(cur.Links, []string{"current"}) {
		t.Fatalf("current: %+v", cur)
	}
	if !reflect.DeepEqual(cur.Hosts.Running, []string{"aa:bb:cc:dd:ee:01"}) || len(cur.Hosts.Pinned) != 0 {
		t.Fatalf("current hosts: %+v (a flatcar host running the same string does not count)", cur.Hosts)
	}
	prev := byVersion["27.01.1"]
	if prev.Retained != RetainedPrevious || !reflect.DeepEqual(prev.Links, []string{"previous"}) || !reflect.DeepEqual(prev.Hosts.Running, []string{"aa:bb:cc:dd:ee:02"}) || !reflect.DeepEqual(prev.Hosts.Pinned, []string{"aa:bb:cc:dd:ee:02"}) {
		t.Fatalf("previous: %+v", prev)
	}
	if lg := byVersion[homelabBluefin]; lg.Retained != RetainedLastGood || !reflect.DeepEqual(lg.Links, []string{"lastGood"}) {
		t.Fatalf("lastGood: %+v", lg)
	}
	if pinned := byVersion[homelabBluefinPr]; pinned.Retained != RetainedPinned || len(pinned.Links) != 0 || !reflect.DeepEqual(pinned.Hosts.Pinned, []string{"aa:bb:cc:dd:ee:04"}) {
		t.Fatalf("pinned: %+v", pinned)
	}
	var wantBF int64 = 2000 + 2
	for _, v := range []string{"27.01.1", "27.02.2", homelabBluefin, homelabBluefinPr} {
		wantBF += manifestBytes(v)
	}
	if bf.Bytes != wantBF {
		t.Fatalf("bluefin bytes=%d want %d", bf.Bytes, wantBF)
	}

	fl := s.OS[OSFlatcar]
	if fl.Channel != "beta" || fl.Pin != homelabFlatcar || len(fl.Releases) != 1 || fl.Releases[0].Bytes != 301 || fl.Releases[0].Retained != RetainedCurrent {
		t.Fatalf("flatcar block: %+v", fl)
	}

	kinds := map[string]string{}
	for _, a := range s.Assets {
		kinds[a.Name] = a.Kind
	}
	want := map[string]string{"ipxe.efi": AssetIPXE, "ipxe-shimx64.efi": AssetShim, "pxelinux.0": AssetSyslinux, "grubx64.efi": AssetGrub, "version.txt": AssetOther, config.FlatcarPinFile: AssetOther, "hardware.json": AssetOther}
	for name, kind := range want {
		if kinds[name] != kind {
			t.Errorf("asset %s kind=%q want %q (all: %v)", name, kinds[name], kind, kinds)
		}
	}
	for _, a := range s.Assets {
		if a.Name == flatcarKernel {
			t.Fatal("a compatibility symlink into a release is not an asset")
		}
	}
	if s.Autopilot.StateBytes != 11 || s.Autopilot.ReportsBytes != 13 {
		t.Fatalf("autopilot: %+v", s.Autopilot)
	}
	if len(s.Other) != 1 || s.Other[0].Name != "secureboot" || s.Other[0].Bytes != 17 {
		t.Fatalf("other: %+v", s.Other)
	}
	if s.Used.Bytes < 2000+300+50+20+7+8+11+13+17 {
		t.Fatalf("used=%d", s.Used.Bytes)
	}

	t.Run("sizes are cached, the join is live", func(t *testing.T) {
		writeBytes(t, filepath.Join(dir, "bluefin", "27.02.2", "extra.raw"), 500)
		if _, err := hardware.Put(hardware.Host{MAC: "aa:bb:cc:dd:ee:05", OS: "bluefin", Running: "27.02.2"}); err != nil {
			t.Fatal(err)
		}
		again := CurrentStorage()
		cur := again.OS[OSBluefin].Releases[1]
		if cur.Bytes != 1000+manifestBytes("27.02.2") {
			t.Fatalf("the walk is memoized for a minute, got %d", cur.Bytes)
		}
		if !reflect.DeepEqual(cur.Hosts.Running, []string{"aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:05"}) {
			t.Fatalf("hosts are joined at request time: %+v", cur.Hosts)
		}
		InvalidateStorage()
		if cur := CurrentStorage().OS[OSBluefin].Releases[1]; cur.Bytes != 1500+manifestBytes("27.02.2") || cur.Files != 6 {
			t.Fatalf("after invalidation: %+v", cur)
		}
	})

	t.Run("an untracked OS still lists its leftovers", func(t *testing.T) {
		viper.Set(config.CoreOSChannel, config.ChannelNone)
		t.Cleanup(func() { viper.Set(config.CoreOSChannel, "stable") })
		co := CurrentStorage().OS[OSCoreOS]
		if co.Tracked || co.Channel != config.ChannelNone || len(co.Releases) != 1 {
			t.Fatalf("coreos block: %+v", co)
		}
		if r := co.Releases[0]; r.Version != homelabCoreOS || r.Cached || r.Retained != RetainedNone || !reflect.DeepEqual(r.Links, []string{"current", "lastGood"}) {
			t.Fatalf("an untracked release shows its links but is neither cached nor retained: %+v", r)
		}
	})

	t.Run("prune invalidates", func(t *testing.T) {
		viper.Set(config.CoreOSChannel, "stable")
		pruneReleases(OSBluefin)
		got := CurrentStorage().OS[OSBluefin]
		if len(got.Releases) != 4 {
			t.Fatalf("every bluefin release is retained: %+v", got.Releases)
		}
	})
}

func TestAssetKind(t *testing.T) {
	for name, want := range map[string]string{
		"undionly.kpxe":       AssetIPXE,
		"snponly.efi":         AssetIPXE,
		"snponly-shimx64.efi": AssetShim,
		"shimx64.efi":         AssetShim,
		"grubx64.efi":         AssetGrub,
		"ldlinux.c32":         AssetSyslinux,
		"stable.json":         AssetOther,
	} {
		if got := assetKind(name); got != want {
			t.Errorf("%s: %q want %q", name, got, want)
		}
	}
}
