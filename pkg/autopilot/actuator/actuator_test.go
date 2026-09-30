package actuator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/cluster/k8s"
	"github.com/jeefy/booty/pkg/cluster/k8s/fake"
	"github.com/jeefy/booty/pkg/hardware"
)

const fixtures = "../../cluster/k8s/testdata"

func loaded(t *testing.T) (*fake.API, *k8s.Client) {
	t.Helper()
	api, client, _ := fake.New(t)
	api.Load(t, fixtures)
	return api, client
}

func noKured(t *testing.T, api *fake.API) {
	t.Helper()
	api.Set("/apis/apps/v1/daemonsets", []byte(`{"items":[{"metadata":{"name":"cilium","namespace":"kube-system"}}]}`))
}

func TestChooseOrder(t *testing.T) {
	ctx := context.Background()
	api, client := loaded(t)
	now := time.Now()
	c := NewChooser(Options{Client: client, SSHKeyPath: "/k", Now: func() time.Time { return now }})
	r, err := c.Choose(ctx)
	if err != nil || r.Name() != NameKured {
		t.Fatalf("kured present: %v %v", r, err)
	}
	st, name := c.Inspect(ctx)
	if !st.Reachable || !st.Kured || st.Nodes != 3 || name != NameKured || st.Error != "" {
		t.Fatalf("inspect: %+v %s", st, name)
	}

	noKured(t, api)
	if r, _ := c.Choose(ctx); r.Name() != NameKured {
		t.Fatal("kured detection must be cached for 5 min")
	}
	now = now.Add(6 * time.Minute)
	r, err = c.Choose(ctx)
	if err != nil || r.Name() != NameAPI {
		t.Fatalf("no kured, API reachable: %v %v", r, err)
	}
	if got := api.Writes(); len(got) != 0 {
		t.Fatalf("choosing and inspecting must never write: %v", got)
	}

	dead := k8s.FromConfig(kubeadmConfig("https://127.0.0.1:1"))
	c = NewChooser(Options{Client: dead, SSHKeyPath: "/k"})
	r, err = c.Choose(ctx)
	if err != nil || r.Name() != NameSSH {
		t.Fatalf("unreachable API with a key: %v %v", r, err)
	}
	if r.(*SSH).Drain != nil {
		t.Fatal("an unreachable API must not be used for the SSH drain")
	}
	st, name = c.Inspect(ctx)
	if st.Reachable || name != NameSSH || st.Error == "" {
		t.Fatalf("inspect unreachable: %+v %s", st, name)
	}

	c = NewChooser(Options{Client: dead})
	if _, err := c.Choose(ctx); !errors.Is(err, ErrNoActuator) {
		t.Fatalf("nothing: %v", err)
	}
	if _, err := c.Kured(ctx); !k8s.IsUnreachable(err) {
		t.Fatalf("the failure must be remembered: %v", err)
	}
	if _, name := c.Inspect(ctx); name != NameNone {
		t.Fatalf("inspect nothing: %s", name)
	}

	c = NewChooser(Options{})
	if _, err := c.Choose(ctx); !errors.Is(err, ErrNoActuator) {
		t.Fatalf("no client, no key: %v", err)
	}
	c = NewChooser(Options{SSHKeyPath: "/k"})
	if r, err := c.Choose(ctx); err != nil || r.Name() != NameSSH {
		t.Fatalf("no client, key: %v %v", r, err)
	}

	api.Fail = 403
	now = time.Now()
	c = NewChooser(Options{Client: client, Now: func() time.Time { return now }})
	r, err = c.Choose(ctx)
	if err != nil || r.Name() != NameAPI {
		t.Fatalf("403 on daemonsets still means the API is there: %v %v", r, err)
	}
	st, _ = c.Inspect(ctx)
	if !st.Reachable || !strings.Contains(st.Error, "403") {
		t.Fatalf("inspect 403: %+v", st)
	}
	api.Fail = 0
	if r, _ := c.Choose(ctx); r.Name() != NameAPI {
		t.Fatal("a failure is remembered for failureTTL")
	}
	api.Load(t, fixtures)
	now = now.Add(failureTTL)
	if r, _ := c.Choose(ctx); r.Name() != NameKured {
		t.Fatal("after failureTTL kured is looked up again")
	}
}

func TestInspectIsCachedForInspectTTL(t *testing.T) {
	ctx := context.Background()
	api, client := loaded(t)
	now := time.Now()
	c := NewChooser(Options{Client: client, Now: func() time.Time { return now }})
	st, name := c.Inspect(ctx)
	if !st.Reachable || st.Nodes != 3 || name != NameKured {
		t.Fatalf("first inspect: %+v %s", st, name)
	}
	listed := len(api.Seen())
	api.Set("/api/v1/nodes", []byte(`{"items":[]}`))
	now = now.Add(30 * time.Second)
	if st, _ := c.Inspect(ctx); st.Nodes != 3 || len(api.Seen()) != listed {
		t.Fatalf("within InspectTTL the answer is reused without a request: %+v (%d requests)", st, len(api.Seen())-listed)
	}
	now = now.Add(31 * time.Second)
	if st, _ := c.Inspect(ctx); st.Nodes != 0 || len(api.Seen()) == listed {
		t.Fatalf("after InspectTTL the cluster is read again: %+v", st)
	}
	if got := api.Writes(); len(got) != 0 {
		t.Fatalf("inspecting must never write: %v", got)
	}
}

func TestResolve(t *testing.T) {
	_, client := loaded(t)
	ctx := context.Background()
	h := &hardware.Host{MAC: "aa:bb:cc:dd:ee:01", Hostname: "aren", OS: "flatcar", IP: "10.0.0.2", Health: &hardware.Health{DMI: hardware.DMI{ProductUUID: "0cccfb00-a3f6-11e4-9504-40a8f0af398d"}}}
	node, err := Resolve(ctx, client, FromHardware(h))
	if err != nil || node.Name != "aren" {
		t.Fatalf("matching uuid: %v %v", node, err)
	}
	h.Health.DMI.ProductUUID = "0CCCFB00-A3F6-11E4-9504-40A8F0AF398D"
	if _, err := Resolve(ctx, client, FromHardware(h)); err != nil {
		t.Fatalf("uuid compare must be case-insensitive: %v", err)
	}
	h.Health.DMI.ProductUUID = "11111111-2222-3333-4444-555555555555"
	if _, err := Resolve(ctx, client, FromHardware(h)); !errors.Is(err, ErrNodeMismatch) {
		t.Fatalf("mismatch: %v", err)
	}
	h.Health = nil
	if _, err := Resolve(ctx, client, FromHardware(h)); err != nil {
		t.Fatalf("no uuid known: hostname alone: %v", err)
	}
	h.Hostname = "ghost"
	if _, err := Resolve(ctx, client, FromHardware(h)); !k8s.IsNotFound(err) {
		t.Fatalf("no such node: %v", err)
	}
	h.Hostname = ""
	if _, err := Resolve(ctx, client, FromHardware(h)); err == nil {
		t.Fatal("no hostname must fail")
	}
}

func TestAPIDrainHonoursPDBs(t *testing.T) {
	api, client := loaded(t)
	ctx := context.Background()
	var slept []time.Duration
	clock := time.Now()
	a := &API{Client: client, DrainTimeout: time.Minute,
		Now: func() time.Time { return clock },
		Sleep: func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			clock = clock.Add(d)
			return nil
		}}
	api.Refuse["default/web-7d9f-x1"] = 2
	host := Host{Hostname: "ehrlitan", OS: "flatcar"}
	if err := a.Prepare(ctx, host); err != nil {
		t.Fatal(err)
	}
	if p := api.Patches["/api/v1/nodes/ehrlitan"]; len(p) != 1 || p[0] != `{"spec":{"unschedulable":true}}` {
		t.Fatalf("cordon: %v", p)
	}
	if api.Evictions["default/web-7d9f-x1"] != 1 || api.Evictions["default/backup-29001-k7"] != 1 || api.Evictions["default/lonely"] != 1 {
		t.Fatalf("evictions: %v", api.Evictions)
	}
	if api.Evictions["kube-system/cilium-abc12"] != 0 || api.Evictions["kube-system/kube-proxy-ehrlitan"] != 0 {
		t.Fatalf("DaemonSet and mirror pods must be skipped: %v", api.Evictions)
	}
	if len(slept) != 2 || slept[0] != time.Second || slept[1] != time.Second {
		t.Fatalf("Retry-After: 1 must drive the back-off: %v", slept)
	}

	api.Refuse["default/web-7d9f-x1"] = 1000
	a.DrainTimeout = 3 * time.Second
	err := a.Drain(ctx, "ehrlitan")
	if err == nil || !strings.Contains(err.Error(), "PodDisruptionBudget") || !strings.Contains(err.Error(), "default/web-7d9f-x1") {
		t.Fatalf("drain timeout: %v", err)
	}

	if err := a.Finish(ctx, host); err != nil {
		t.Fatal(err)
	}
	if p := api.Patches["/api/v1/nodes/ehrlitan"]; len(p) != 2 || p[1] != `{"spec":{"unschedulable":false}}` {
		t.Fatalf("uncordon: %v", p)
	}
}

func TestAPIReboot(t *testing.T) {
	api, client := loaded(t)
	ctx := context.Background()
	host := Host{Hostname: "ehrlitan", OS: "flatcar"}
	a := &API{Client: client, Namespace: "kube-system"}
	if err := a.Reboot(ctx, host); err == nil || !strings.Contains(err.Error(), "BOOTY_IMAGE") {
		t.Fatalf("unknown image must be refused before any API call: %v", err)
	}
	a = &API{Client: client, Image: "ghcr.io/jeefy/booty:main"}
	if err := a.Reboot(ctx, host); err == nil || !strings.Contains(err.Error(), "POD_NAMESPACE") {
		t.Fatalf("unknown namespace: %v", err)
	}
	if got := api.Writes(); len(got) != 0 {
		t.Fatalf("refusals must not write: %v", got)
	}
	a.Namespace = "kube-system"
	if err := a.Reboot(ctx, host); err != nil {
		t.Fatal(err)
	}
	if len(api.Deleted) != 1 || api.Deleted[0] != "kube-system/booty-node-reboot-ehrlitan" {
		t.Fatalf("previous pod cleanup: %v", api.Deleted)
	}
	created := api.Created["kube-system"]
	if len(created) != 1 {
		t.Fatalf("created: %v", api.Created)
	}
	var pod struct {
		Metadata struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			NodeName              string `json:"nodeName"`
			HostPID               bool   `json:"hostPID"`
			RestartPolicy         string `json:"restartPolicy"`
			ActiveDeadlineSeconds int    `json:"activeDeadlineSeconds"`
			Automount             *bool  `json:"automountServiceAccountToken"`
			Tolerations           []struct {
				Operator string `json:"operator"`
			} `json:"tolerations"`
			Containers []struct {
				Image           string   `json:"image"`
				Command         []string `json:"command"`
				SecurityContext struct {
					Privileged bool `json:"privileged"`
				} `json:"securityContext"`
			} `json:"containers"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(created[0], &pod); err != nil {
		t.Fatal(err)
	}
	c := pod.Spec.Containers[0]
	if pod.Metadata.Name != "booty-node-reboot-ehrlitan" || pod.Spec.NodeName != "ehrlitan" || !pod.Spec.HostPID || pod.Spec.RestartPolicy != "Never" || pod.Spec.ActiveDeadlineSeconds != 120 || pod.Spec.Automount == nil || *pod.Spec.Automount {
		t.Fatalf("pod spec: %s", created[0])
	}
	if len(pod.Spec.Tolerations) != 1 || pod.Spec.Tolerations[0].Operator != "Exists" {
		t.Fatalf("tolerations: %s", created[0])
	}
	if c.Image != "ghcr.io/jeefy/booty:main" || strings.Join(c.Command, " ") != "/booty node-reboot" || !c.SecurityContext.Privileged {
		t.Fatalf("container: %s", created[0])
	}
	if pod.Metadata.Labels["booty.jeefy.dev/node"] != "ehrlitan" {
		t.Fatalf("labels: %v", pod.Metadata.Labels)
	}
}

func TestEvictable(t *testing.T) {
	cases := map[string]struct {
		pod  k8s.Pod
		want bool
	}{
		"replicaset": {k8s.Pod{Owner: "ReplicaSet", Phase: "Running"}, true},
		"bare":       {k8s.Pod{Phase: "Pending"}, true},
		"job":        {k8s.Pod{Owner: "Job", Phase: "Running"}, true},
		"daemonset":  {k8s.Pod{Owner: "DaemonSet", Phase: "Running"}, false},
		"mirror":     {k8s.Pod{Mirror: true, Owner: "Node", Phase: "Running"}, false},
		"succeeded":  {k8s.Pod{Owner: "Job", Phase: "Succeeded"}, false},
		"failed":     {k8s.Pod{Phase: "Failed"}, false},
	}
	for name, c := range cases {
		if got := Evictable(c.pod); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestRebootSignalIsGlibcSIGRTMINPlus5(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	out, err := exec.Command("bash", "-c", "kill -l RTMIN+5").Output()
	if err != nil {
		t.Skip("bash cannot resolve RTMIN+5")
	}
	if strings.TrimSpace(string(out)) != "39" || int(RebootSignal) != 39 {
		t.Fatalf("bash says SIGRTMIN+5 = %s, RebootSignal = %d", out, RebootSignal)
	}
}

func TestNodeRebootSignalsTheTarget(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not installed")
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	synced := false
	if err := NodeReboot(cmd.Process.Pid, func() { synced = true }, nil); err != nil {
		t.Fatal(err)
	}
	if !synced {
		t.Fatal("sync must run before the signal")
	}
	err := cmd.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("sleep must have been killed: %v", err)
	}
	ws, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != RebootSignal {
		t.Fatalf("sleep must die of SIGRTMIN+5, got %v", exit)
	}

	var got syscall.Signal
	gotPid := 0
	if err := NodeReboot(1, func() {}, func(pid int, sig syscall.Signal) error { gotPid, got = pid, sig; return nil }); err != nil {
		t.Fatal(err)
	}
	if gotPid != 1 || got != RebootSignal {
		t.Fatalf("kill(%d, %d)", gotPid, got)
	}
	if err := NodeReboot(1, func() {}, func(int, syscall.Signal) error { return os.ErrPermission }); err == nil || !strings.Contains(err.Error(), "SIGRTMIN+5") {
		t.Fatalf("kill failure must surface: %v", err)
	}
}
