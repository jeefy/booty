package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/jeefy/booty/pkg/state"
)

func TestInfoSyncErrors(t *testing.T) {
	srv, _ := newTestServer(t)
	state.ClearSyncError("flatcar")
	t.Cleanup(func() { state.ClearSyncError("flatcar") })

	r := do(t, http.MethodGet, srv.URL+"/info", "")
	var info map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.body), &info); err != nil {
		t.Fatal(err)
	}
	if _, ok := info["syncErrors"]; ok {
		t.Fatalf("syncErrors must be omitted when empty: %s", r.body)
	}

	state.SetSyncError("flatcar", errors.New("download failed: 503"))
	r = do(t, http.MethodGet, srv.URL+"/info", "")
	var withErrs struct {
		SyncErrors map[string]state.SyncError `json:"syncErrors"`
	}
	if err := json.Unmarshal([]byte(r.body), &withErrs); err != nil {
		t.Fatal(err)
	}
	got, ok := withErrs.SyncErrors["flatcar"]
	if !ok || got.Error != "download failed: 503" || got.At.IsZero() {
		t.Fatalf("syncErrors = %+v in %s", withErrs.SyncErrors, r.body)
	}
}
