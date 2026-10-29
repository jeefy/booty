package versions

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/state"
)

func TestSweepStaleTemp(t *testing.T) {
	dir := setupVerify(t)
	write := func(rel string, age time.Duration) string {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(-age)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
		return p
	}
	fresh := write("flatcar/1.0/kernel.1.tmp", 0)
	busy := write("coreos/1.0/kernel.2.tmp", 0)
	topFresh := write("secureboot/x.3.tmp", 0)
	topOld := write("secureboot/y.4.tmp", time.Hour)
	keep := write("flatcar/1.0/kernel", time.Hour)

	unlock, ok := state.TryLockCoreOSUpdate()
	if !ok {
		t.Fatal("coreos lock busy")
	}
	SweepStaleTemp()
	unlock()

	for p, want := range map[string]bool{fresh: false, busy: true, topFresh: true, topOld: false, keep: true} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s exists=%v want %v", p, err == nil, want)
		}
	}
}
