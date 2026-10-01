package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/autopilot"
	"github.com/jeefy/booty/pkg/autopilot/actuator"
	"github.com/jeefy/booty/pkg/autopilot/controller"
	"github.com/jeefy/booty/pkg/cluster/k8s/fake"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/state"
	"github.com/jeefy/booty/pkg/versions"
)

type autopilotStatus struct {
	Mode    string `json:"mode"`
	Cluster struct {
		Reachable bool   `json:"reachable"`
		Kured     bool   `json:"kured"`
		Nodes     int    `json:"nodes"`
		Error     string `json:"error"`
	} `json:"cluster"`
	Actuator string            `json:"actuator"`
	SSHUsers map[string]string `json:"sshUsers"`
	DryRun   bool              `json:"dryRun"`
	OS       map[string]struct {
		FleetTarget string `json:"fleetTarget"`
		Current     string `json:"current"`
		LastGood    string `json:"lastGood"`
		Held        bool   `json:"held"`
		Releases    []struct {
			Version string `json:"version"`
			State   string `json:"state"`
		} `json:"releases"`
	} `json:"os"`
	Hosts []struct {
		MAC     string `json:"mac"`
		Episode *struct {
			State   string `json:"state"`
			Attempt int    `json:"attempt"`
			Class   string `json:"class"`
			Target  string `json:"target"`
		} `json:"episode"`
	} `json:"hosts"`
	Events  []struct{ Text string } `json:"events"`
	Reports []struct {
		Key       string `json:"key"`
		Draft     bool   `json:"draft"`
		Path      string `json:"path"`
		UpdatedAt string `json:"updatedAt"`
		PostedURL string `json:"postedURL"`
		Class     string `json:"class"`
		Attempts  int    `json:"attempts"`
	} `json:"reports"`
}

func getAutopilot(t *testing.T) autopilotStatus {
	t.Helper()
	rec := httptest.NewRecorder()
	handleAutopilotRequest(rec, httptest.NewRequest(http.MethodGet, "/autopilot", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var st autopilotStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	return st
}

// TestAutopilotEndpointIsReadOnly is the P2 guarantee kept in P3: GET
// /autopilot with a live cluster client reports the cluster and the
// actuator, and the fake API sees nothing but GETs.
func TestAutopilotEndpointIsReadOnly(t *testing.T) {
	t.Cleanup(func() { setAutopilot(nil) })

	setAutopilot(nil)
	if st := getAutopilot(t); st.Mode != config.AutopilotOff || st.Actuator != actuator.NameNone || !st.DryRun || st.OS != nil {
		t.Fatalf("no autopilot: %+v", st)
	}

	api, client, _ := fake.New(t)
	api.Load(t, "../cluster/k8s/testdata")
	pilot := &autopilot.Autopilot{
		Settings: autopilot.Settings{Mode: config.AutopilotGuard, Namespace: "kube-system"},
		Client:   client,
		Chooser:  actuator.NewChooser(actuator.Options{Client: client, Namespace: "kube-system"}),
	}
	setAutopilot(pilot)
	st := getAutopilot(t)
	if st.Mode != "guard" || !st.Cluster.Reachable || !st.Cluster.Kured || st.Cluster.Nodes != 3 || st.Actuator != actuator.NameKured || !st.DryRun || st.SSHUsers != nil {
		t.Fatalf("kured cluster: %+v", st)
	}

	rec := httptest.NewRecorder()
	handleAutopilotRequest(rec, httptest.NewRequest(http.MethodPost, "/autopilot", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST must be refused: %d", rec.Code)
	}

	dead, err := autopilot.Setup(autopilot.Settings{Mode: config.AutopilotGuard, Kubeconfig: writeBogusKubeconfig(t), SSHKey: "/dev/null", HealthWindow: time.Minute, RetryAfter: time.Minute}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dead.Controller == nil {
		t.Fatal("guard must build a controller")
	}
	setAutopilot(dead)
	st = getAutopilot(t)
	if st.Cluster.Reachable || st.Cluster.Kured || st.Cluster.Nodes != 0 || st.Actuator != actuator.NameSSH || st.Cluster.Error == "" || st.SSHUsers["bluefin"] != "core" || st.SSHUsers["flatcar"] != "core" || st.DryRun {
		t.Fatalf("bogus server: %+v", st)
	}

	if got := api.Writes(); len(got) != 0 {
		t.Fatalf("GET /autopilot must never write to the cluster: %v", got)
	}
}

func TestAutopilotOffBuildsNoController(t *testing.T) {
	a, err := autopilot.Setup(autopilot.Settings{Mode: config.AutopilotOff}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Controller != nil {
		t.Fatal("off must not build a controller")
	}
	if s := a.Summary(); s.Mode != config.AutopilotOff || s.Actuator != actuator.NameNone || len(s.Held) != 0 {
		t.Fatalf("summary: %+v", s)
	}
}

// TestAutopilotSignalsReachTheController drives a Flatcar host through the
// real handlers (/booty.ipxe, /ignition.json, /booted, /health) with the
// controller behind them and checks the episode, the update-check rule,
// /info.autopilot and the clear endpoint.
func TestAutopilotSignalsReachTheController(t *testing.T) {
	srv, dir := newTestServer(t)
	t.Cleanup(func() {
		setAutopilot(nil)
		versions.HoldFleetTarget("flatcar", "")
		state.SetCurrentFlatcarVersion("")
	})
	for _, v := range []string{"4800.0.0", "4757.2.0"} {
		writeRelease(t, dir, "flatcar", v, "flatcar_production_pxe.vmlinuz", "flatcar_production_pxe_image.cpio.gz")
	}
	if err := os.Symlink("4800.0.0", filepath.Join(dir, "flatcar", "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("4757.2.0", filepath.Join(dir, "flatcar", "lastGood")); err != nil {
		t.Fatal(err)
	}
	state.SetCurrentFlatcarVersion("4800.0.0")

	ctrl, err := controller.New(controller.Options{Mode: config.AutopilotGuard, Fleet: controller.LiveFleet{}, StatePath: filepath.Join(dir, "autopilot", "state.json"), ReportsDir: filepath.Join(dir, "autopilot", "reports"), HealthWindow: 15 * time.Minute, RetryAfter: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	setAutopilot(&autopilot.Autopilot{Settings: autopilot.Settings{Mode: config.AutopilotGuard, HealthWindow: 15 * time.Minute, RetryAfter: time.Hour}, Controller: ctrl})

	const mac = "aa:bb:cc:dd:ee:01"
	register(t, srv.URL, `{"mac":"`+mac+`","hostname":"n1","os":"flatcar","canary":true}`)
	h, _ := hardware.Get(mac)
	if !h.Canary {
		t.Fatalf("canary must round-trip through /register: %+v", h)
	}

	if r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+mac, ""); r.status != 200 {
		t.Fatalf("%+v", r)
	}
	if r := do(t, http.MethodGet, srv.URL+"/ignition.json?mac="+mac, ""); r.status != 200 {
		t.Fatalf("%+v", r)
	}
	st := getAutopilot(t)
	if len(st.Hosts) != 1 || st.Hosts[0].Episode == nil || st.Hosts[0].Episode.State != hardware.AutopilotGating || st.Hosts[0].Episode.Target != "4800.0.0" {
		t.Fatalf("episode after the boot fetches: %+v", st.Hosts)
	}
	if st.OS["flatcar"].FleetTarget != "4800.0.0" || st.OS["flatcar"].LastGood != "4757.2.0" || st.OS["flatcar"].Held {
		t.Fatalf("os block: %+v", st.OS["flatcar"])
	}

	if r := do(t, http.MethodPost, srv.URL+"/booted?mac="+mac, ""); r.status != 200 {
		t.Fatalf("%+v", r)
	}
	if r := do(t, http.MethodPost, srv.URL+"/health?mac="+mac, `{"running":"4800.0.0","failedUnits":["kubelet.service"],"firmware":"bios"}`); r.status != 200 {
		t.Fatalf("%+v", r)
	}
	st = getAutopilot(t)
	e := st.Hosts[0].Episode
	if e.State != hardware.AutopilotRetrying || e.Attempt != 2 || e.Class != controller.ClassFailedUnits {
		t.Fatalf("episode after failed units: %+v", e)
	}
	if !st.OS["flatcar"].Held || st.OS["flatcar"].FleetTarget != "4757.2.0" {
		t.Fatalf("fleet must be held at lastGood after the first failure: %+v", st.OS["flatcar"])
	}
	if got := versions.FleetTarget("flatcar"); got != "4757.2.0" {
		t.Fatalf("versions.FleetTarget must follow the hold: %s", got)
	}

	r := do(t, http.MethodGet, srv.URL+"/update-check?mac="+mac+"&os=flatcar&version=4800.0.0", "")
	var resp updateCheckResponse
	if err := json.Unmarshal([]byte(r.body), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.RebootRequired || resp.Target != "4800.0.0" || resp.Reason == "" {
		t.Fatalf("update-check must ask for attempt 2 into the same release: %+v", resp)
	}

	ctrl.Tick(t.Context())
	h, _ = hardware.Get(mac)
	if h.Autopilot == nil || h.Autopilot.State != hardware.AutopilotRetrying || h.Autopilot.Attempt != 2 || !h.Autopilot.Pinned || h.TargetVersion != "4800.0.0" {
		t.Fatalf("host summary: %+v targetVersion=%q", h.Autopilot, h.TargetVersion)
	}
	register(t, srv.URL, `{"mac":"`+mac+`","hostname":"n1","os":"flatcar","targetVersion":"4800.0.0"}`)
	h, _ = hardware.Get(mac)
	if h.Autopilot == nil || h.Autopilot.State != hardware.AutopilotRetrying {
		t.Fatalf("/register must keep the server-owned autopilot summary: %+v", h.Autopilot)
	}

	r = do(t, http.MethodGet, srv.URL+"/info", "")
	var info struct {
		Autopilot struct {
			Mode       string   `json:"mode"`
			Actuator   string   `json:"actuator"`
			Held       []string `json:"held"`
			Quarantine int      `json:"quarantined"`
			NeedsHands int      `json:"needsHands"`
		} `json:"autopilot"`
		Targets struct {
			Flatcar string `json:"flatcar"`
			CoreOS  string `json:"coreos"`
			Bluefin string `json:"bluefin"`
		} `json:"targets"`
	}
	if err := json.Unmarshal([]byte(r.body), &info); err != nil {
		t.Fatal(err)
	}
	if info.Autopilot.Mode != "guard" || info.Autopilot.Actuator != "none" || len(info.Autopilot.Held) != 1 || info.Autopilot.Held[0] != "flatcar" {
		t.Fatalf("/info.autopilot: %+v", info.Autopilot)
	}
	if info.Targets.Flatcar != "4757.2.0" || info.Targets.CoreOS != "" || info.Targets.Bluefin != "" {
		t.Fatalf("/info.targets must follow the fleet hold: %+v", info.Targets)
	}

	for path, want := range map[string]int{
		"/autopilot/flatcar/release/4800.0.0/clear": http.StatusConflict,
		"/autopilot/flatcar/release/4800.0.0/nope":  http.StatusNotFound,
		"/autopilot/nacl/release/4800.0.0/clear":    http.StatusBadRequest,
		"/autopilot/flatcar/release/v..1/clear":     http.StatusBadRequest,
	} {
		if r := do(t, http.MethodPost, srv.URL+path, ""); r.status != want {
			t.Fatalf("POST %s: %d (%s), want %d", path, r.status, r.body, want)
		}
	}
	if r := do(t, http.MethodGet, srv.URL+"/autopilot/flatcar/release/4800.0.0/clear", ""); r.status != http.StatusMethodNotAllowed {
		t.Fatalf("GET clear: %+v", r)
	}

	if r := do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+mac, ""); r.status != 200 {
		t.Fatalf("%+v", r)
	}
	do(t, http.MethodGet, srv.URL+"/ignition.json?mac="+mac, "")
	do(t, http.MethodPost, srv.URL+"/booted?mac="+mac, "")
	do(t, http.MethodPost, srv.URL+"/health?mac="+mac, `{"running":"4800.0.0","failedUnits":["kubelet.service"],"journalErrors":["n1 kernel: link 40:a8:f0:12:34:56 at 192.168.1.57"]}`)
	do(t, http.MethodGet, srv.URL+"/booty.ipxe?mac="+mac, "")
	do(t, http.MethodGet, srv.URL+"/ignition.json?mac="+mac, "")
	do(t, http.MethodPost, srv.URL+"/booted?mac="+mac, "")
	do(t, http.MethodPost, srv.URL+"/health?mac="+mac, `{"running":"4757.2.0","failedUnits":[],"firmware":"uefi","dmi":{"vendor":"HP","product":"EliteDesk","productUUID":"4c4c4544-0031-3310-8052-b6c04f4d3732"},"journalErrors":["n1 systemd[1]: lastGood boot noise that must stay out of the report"]}`)
	ctrl.Tick(t.Context())
	st = getAutopilot(t)
	if got := st.OS["flatcar"].Releases; len(got) != 2 || got[0].Version != "4800.0.0" || got[0].State != controller.ReleaseTimeout {
		t.Fatalf("release after rollback: %+v", got)
	}
	if len(st.Reports) != 1 || st.Reports[0].Key != "flatcar-4800.0.0" || !st.Reports[0].Draft || st.Reports[0].Path != "/autopilot/reports/flatcar-4800.0.0.md" || st.Reports[0].UpdatedAt == "" || st.Reports[0].PostedURL != "" || st.Reports[0].Class != controller.ClassFailedUnits || st.Reports[0].Attempts != 3 {
		t.Fatalf("report summary: %+v", st.Reports)
	}
	r = do(t, http.MethodGet, srv.URL+st.Reports[0].Path, "")
	if r.status != 200 || !strings.HasPrefix(r.contentType, "text/markdown") || !strings.Contains(r.body, "<!-- booty-autopilot: flatcar 4800.0.0 ") || !strings.Contains(r.body, "<host> kernel: link <mac> at <ip>") {
		t.Fatalf("report markdown: %+v", r)
	}
	if strings.Contains(r.body, "n1") || strings.Contains(r.body, "192.168.1.57") || strings.Contains(r.body, "40:a8") || strings.Contains(r.body, "4c4c4544") {
		t.Fatalf("served report leaks identifiers: %s", r.body)
	}
	if strings.Contains(r.body, "lastGood boot noise") {
		t.Fatalf("the rollback boot's journal must not be attributed to the bad release: %s", r.body)
	}
	if r := do(t, http.MethodGet, srv.URL+"/autopilot/reports/flatcar-4800.0.0.json", ""); r.status != 200 || !strings.HasPrefix(r.contentType, "application/json") || !strings.Contains(r.body, `"marker"`) {
		t.Fatalf("report json: %+v", r)
	}
	for path, want := range map[string]int{
		"/autopilot/reports/flatcar-4800.0.0":          http.StatusNotFound,
		"/autopilot/reports/flatcar-4800.0.0.txt":      http.StatusNotFound,
		"/autopilot/reports/..%2F..%2Fstate.json":      http.StatusNotFound,
		"/autopilot/reports/../state.json":             http.StatusNotFound,
		"/autopilot/reports/flatcar-9999.0.0.md":       http.StatusNotFound,
		"/autopilot/reports/Flatcar-4800.0.0.md":       http.StatusNotFound,
		"/autopilot/reports/flatcar-4800.0.0.md/x":     http.StatusNotFound,
		"/autopilot/reports/coreos-44.20260913.2.1.md": http.StatusNotFound,
	} {
		if r := do(t, http.MethodGet, srv.URL+path, ""); r.status != want {
			t.Fatalf("GET %s: %d, want %d", path, r.status, want)
		}
	}
	if r := do(t, http.MethodPost, srv.URL+"/autopilot/reports/flatcar-4800.0.0.md", ""); r.status != http.StatusMethodNotAllowed {
		t.Fatalf("POST report: %d", r.status)
	}
	if r := do(t, http.MethodPost, srv.URL+"/autopilot/flatcar/release/4800.0.0/clear", ""); r.status != 200 {
		t.Fatalf("clear: %+v", r)
	}
	st = getAutopilot(t)
	if st.OS["flatcar"].Releases[0].State != controller.ReleaseRolling || st.OS["flatcar"].Held {
		t.Fatalf("after clear: %+v", st.OS["flatcar"])
	}
	if _, err := os.Stat(filepath.Join(dir, "autopilot", "state.json")); err != nil {
		t.Fatalf("state must be persisted: %v", err)
	}
	for _, ev := range st.Events {
		if strings.Contains(ev.Text, "n1") || strings.Contains(ev.Text, mac) || strings.Contains(ev.Text, "127.0.0.1") {
			t.Fatalf("event text carries an identifier: %q", ev.Text)
		}
	}
}

func writeBogusKubeconfig(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/kubeconfig"
	kc := "apiVersion: v1\nkind: Config\ncurrent-context: c\nclusters:\n- name: c\n  cluster:\n    server: https://127.0.0.1:1\ncontexts:\n- name: c\n  context:\n    cluster: c\n    user: u\nusers:\n- name: u\n  user:\n    token: nope\n"
	if err := os.WriteFile(path, []byte(kc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
