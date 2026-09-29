package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jeefy/booty/pkg/autopilot"
	"github.com/jeefy/booty/pkg/autopilot/actuator"
	"github.com/jeefy/booty/pkg/cluster/k8s/fake"
	"github.com/jeefy/booty/pkg/config"
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

// TestAutopilotEndpointIsReadOnly is the P2 guarantee: GET /autopilot with a
// live cluster client reports the cluster and the actuator, and the fake API
// sees nothing but GETs.
func TestAutopilotEndpointIsReadOnly(t *testing.T) {
	t.Cleanup(func() { setAutopilot(nil) })

	setAutopilot(nil)
	if st := getAutopilot(t); st.Mode != config.AutopilotOff || st.Actuator != actuator.NameNone || !st.DryRun {
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

	dead, err := autopilot.Setup(autopilot.Settings{Mode: config.AutopilotGuard, Kubeconfig: writeBogusKubeconfig(t), SSHKey: "/dev/null"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	setAutopilot(dead)
	st = getAutopilot(t)
	if st.Cluster.Reachable || st.Cluster.Kured || st.Cluster.Nodes != 0 || st.Actuator != actuator.NameSSH || st.Cluster.Error == "" || st.SSHUsers["bluefin"] != "root" || st.SSHUsers["flatcar"] != "core" {
		t.Fatalf("bogus server: %+v", st)
	}

	if got := api.Writes(); len(got) != 0 {
		t.Fatalf("GET /autopilot must never write to the cluster: %v", got)
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
