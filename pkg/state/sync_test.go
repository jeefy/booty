package state

import (
	"errors"
	"testing"
)

func TestTryLockUpdate(t *testing.T) {
	for name, fn := range map[string]func() (func(), bool){
		"flatcar": TryLockFlatcarUpdate, "coreos": TryLockCoreOSUpdate, "bluefin": TryLockBluefinUpdate,
	} {
		unlock, ok := fn()
		if !ok {
			t.Fatalf("%s: first lock failed", name)
		}
		if _, ok2 := fn(); ok2 {
			t.Fatalf("%s: second lock succeeded", name)
		}
		unlock()
		u, ok := fn()
		if !ok {
			t.Fatalf("%s: relock failed", name)
		}
		u()
	}
}

func TestSyncErrors(t *testing.T) {
	SetSyncError("flatcar", errors.New("boom"))
	got := SyncErrors()
	if got["flatcar"].Error != "boom" || got["flatcar"].At.IsZero() {
		t.Fatalf("got %+v", got)
	}
	got["flatcar"] = SyncError{}
	if SyncErrors()["flatcar"].Error != "boom" {
		t.Fatal("SyncErrors returned shared map")
	}
	ClearSyncError("flatcar")
	if _, ok := SyncErrors()["flatcar"]; ok {
		t.Fatal("not cleared")
	}
}
