package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/state"
	"github.com/spf13/viper"
)

func TestUntrackedCoreOS(t *testing.T) {
	srv, dir := newTestServer(t)
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:01","hostname":"c1","os":"coreos"}`)
	writeRelease(t, dir, "coreos", "44.20260829.3.1", "fedora-coreos-44.20260829.3.1-live-kernel-x86_64")
	state.SetCurrentCoreOSVersion("44.20260829.3.1")
	t.Cleanup(func() { state.SetCurrentCoreOSVersion("") })

	viper.Set(config.CoreOSChannel, config.ChannelNone)
	t.Cleanup(func() { viper.Set(config.CoreOSChannel, "stable") })

	r := do(t, http.MethodPost, srv.URL+"/register", `{"mac":"aa:bb:cc:dd:ee:02","hostname":"c2","os":"coreos"}`)
	assertJSONError(t, r, http.StatusBadRequest)
	if !strings.Contains(r.body, "--coreOSChannel=none") {
		t.Fatalf("the refusal names the flag: %s", r.body)
	}
	if _, ok := hardware.Get("aa:bb:cc:dd:ee:02"); ok {
		t.Fatal("a refused host must not be saved")
	}
	register(t, srv.URL, `{"mac":"aa:bb:cc:dd:ee:03","hostname":"f1","os":"flatcar"}`)

	r = do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac=aa:bb:cc:dd:ee:01", "")
	if r.status != 200 || !strings.Contains(r.body, "OS not tracked") || !strings.Contains(r.body, "coreos is not tracked by this Booty (--coreOSChannel=none)") || strings.Contains(r.body, "fedora-coreos") {
		t.Fatalf("an already registered coreos host gets the refusal menu:\n%+v", r)
	}
	if strings.Contains(r.body, "[[") {
		t.Fatalf("placeholders left: %s", r.body)
	}

	r = do(t, http.MethodGet, srv.URL+"/update-check?mac=aa:bb:cc:dd:ee:01&os=coreos&version=44.20260101.0.0", "")
	var resp updateCheckResponse
	if err := json.Unmarshal([]byte(r.body), &resp); err != nil {
		t.Fatal(err)
	}
	if r.status != 200 || resp.RebootRequired || resp.Target != "" || !strings.Contains(resp.Reason, "no coreos version") {
		t.Fatalf("update-check behaves as if no release were cached: %+v", resp)
	}

	r = do(t, http.MethodGet, srv.URL+"/info", "")
	var info infoResponse
	if err := json.Unmarshal([]byte(r.body), &info); err != nil {
		t.Fatal(err)
	}
	if info.Targets.CoreOS != "" || info.CoreOS.Version != "" {
		t.Fatalf("/info reports no coreos: targets=%q version=%q", info.Targets.CoreOS, info.CoreOS.Version)
	}
	r = do(t, http.MethodGet, srv.URL+"/version.txt", "")
	if !strings.Contains(r.body, "COREOS_VERSION=\n") {
		t.Fatalf("version.txt: %s", r.body)
	}
}
