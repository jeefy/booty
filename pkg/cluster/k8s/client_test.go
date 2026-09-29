package k8s_test

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/cluster/k8s"
	"github.com/jeefy/booty/pkg/cluster/k8s/fake"
	"github.com/jeefy/booty/pkg/kubeadm"
)

func loaded(t *testing.T) (*fake.API, *k8s.Client) {
	t.Helper()
	api, client, _ := fake.New(t)
	api.Load(t, "testdata")
	return api, client
}

func TestNodes(t *testing.T) {
	_, client := loaded(t)
	nodes, err := client.Nodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Fatalf("nodes: %d", len(nodes))
	}
	cp := nodes[0]
	if cp.Name != "archives" || !cp.Ready || !cp.ControlPlane || cp.Unschedulable || cp.ReadySince.IsZero() || cp.ReadyReason != "KubeletReady" {
		t.Fatalf("archives: %+v", cp)
	}
	if cp.NodeInfo.SystemUUID != "186e93ca-698e-3912-ac80-d8bbc1960f11" || cp.NodeInfo.KubeletVersion != "v1.34.12" || cp.NodeInfo.OSImage != "Ubuntu 24.04.5 LTS" || cp.NodeInfo.KernelVersion != "6.8.0-85-generic" || cp.NodeInfo.ContainerRuntimeVersion != "containerd://1.7.27" {
		t.Fatalf("nodeInfo: %+v", cp.NodeInfo)
	}
	if len(cp.Kured) != 1 || cp.Kured["weave.works/kured-most-recent-reboot-needed"] != "2026-09-19T00:06:34Z" {
		t.Fatalf("kured annotations must be filtered from the rest: %v", cp.Kured)
	}
	aren := nodes[1]
	if aren.Ready || !aren.Unschedulable || aren.ControlPlane || aren.Kured["weave.works/kured-reboot-in-progress"] == "" || aren.ReadyReason != "KubeletNotReady" {
		t.Fatalf("aren: %+v", aren)
	}
	one, err := client.Node(context.Background(), "aren")
	if err != nil || one.Name != "aren" || !one.Unschedulable {
		t.Fatalf("Node(aren): %+v %v", one, err)
	}
	if _, err := client.Node(context.Background(), "nope"); !k8s.IsNotFound(err) {
		t.Fatalf("missing node must be ErrNotFound: %v", err)
	}
}

func TestPods(t *testing.T) {
	_, client := loaded(t)
	pods, err := client.Pods(context.Background(), "ehrlitan")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]k8s.Pod{}
	for _, p := range pods {
		byName[p.Name] = p
	}
	if len(pods) != 5 {
		t.Fatalf("pods: %d", len(pods))
	}
	if c := byName["cilium-abc12"]; c.Owner != "DaemonSet" || c.OwnerName != "cilium" || c.Restarts != 2 || c.Ready != 1 || c.Containers != 1 || !c.Healthy() {
		t.Fatalf("cilium: %+v", c)
	}
	if w := byName["web-7d9f-x1"]; w.Owner != "ReplicaSet" || w.Containers != 2 || w.Ready != 2 || !w.Healthy() {
		t.Fatalf("web: %+v", w)
	}
	if j := byName["backup-29001-k7"]; j.Owner != "Job" || j.Phase != "Pending" || j.Waiting != "ContainerCreating" || j.Healthy() {
		t.Fatalf("job: %+v", j)
	}
	if m := byName["kube-proxy-ehrlitan"]; !m.Mirror || m.Owner != "Node" {
		t.Fatalf("mirror: %+v", m)
	}
	if l := byName["lonely"]; l.Owner != "" || !l.Healthy() {
		t.Fatalf("bare pod: %+v", l)
	}
	if none, err := client.Pods(context.Background(), "other"); err != nil || len(none) != 0 {
		t.Fatalf("a node without pods lists none: %v %v", none, err)
	}
}

func TestDaemonSetsAndKured(t *testing.T) {
	api, client := loaded(t)
	sets, err := client.DaemonSets(context.Background())
	if err != nil || len(sets) != 3 {
		t.Fatalf("daemonsets: %v %v", sets, err)
	}
	present, err := client.KuredPresent(context.Background())
	if err != nil || !present {
		t.Fatalf("kured by name must be detected: %v %v", present, err)
	}
	data, err := os.ReadFile("testdata/daemonsets-nokured.json")
	if err != nil {
		t.Fatal(err)
	}
	api.Set("/apis/apps/v1/daemonsets", data)
	present, err = client.KuredPresent(context.Background())
	if err != nil || !present {
		t.Fatalf("kured by app.kubernetes.io/name label must be detected: %v %v", present, err)
	}
	api.Set("/apis/apps/v1/daemonsets", []byte(`{"items":[{"metadata":{"name":"cilium","namespace":"kube-system"}}]}`))
	present, err = client.KuredPresent(context.Background())
	if err != nil || present {
		t.Fatalf("no kured: %v %v", present, err)
	}
	if ds := (k8s.DaemonSet{Name: "x", Labels: map[string]string{"app": "kured"}}); !ds.IsKured() {
		t.Fatal("app=kured label")
	}
}

func TestTypedErrors(t *testing.T) {
	api, client := loaded(t)
	api.Fail = http.StatusForbidden
	_, err := client.Nodes(context.Background())
	if !k8s.IsForbidden(err) || k8s.IsUnreachable(err) || k8s.IsNotFound(err) {
		t.Fatalf("403 must be ErrForbidden: %v", err)
	}
	var se *kubeadm.StatusError
	if !errors.As(err, &se) || se.Code != 403 || !strings.Contains(err.Error(), "scripted failure") {
		t.Fatalf("status error detail lost: %v", err)
	}

	dead := k8s.FromConfig(kubeadm.KubeConfig{APIServer: "https://127.0.0.1:1", Token: "x"})
	_, err = dead.Nodes(context.Background())
	if !k8s.IsUnreachable(err) || k8s.IsForbidden(err) {
		t.Fatalf("connection refused must be ErrUnreachable: %v", err)
	}

	none := k8s.InCluster()
	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
		if none.Configured() {
			t.Fatal("no in-cluster env must mean not configured")
		}
		_, err = none.Nodes(context.Background())
		if !k8s.IsUnreachable(err) || !errors.Is(err, k8s.ErrNotInCluster) {
			t.Fatalf("outside a cluster: %v", err)
		}
	}
	var nilClient *k8s.Client
	if nilClient.Configured() || nilClient.APIServer() != "" {
		t.Fatal("nil client must read as unconfigured")
	}
}

func TestNodeHealthy(t *testing.T) {
	api, client := loaded(t)
	ctx := context.Background()
	ref, err := client.NodeHealthy(ctx, "ehrlitan", k8s.HealthOptions{})
	if err != nil {
		t.Fatalf("Ready node with a Pending Job pod must be healthy: %v", err)
	}
	if len(ref.Pods) != 5 || ref.Taken.IsZero() {
		t.Fatalf("sample: %+v", ref)
	}

	if _, err := client.NodeHealthy(ctx, "aren", k8s.HealthOptions{}); !errors.Is(err, k8s.ErrUnhealthy) || !strings.Contains(err.Error(), "not Ready") {
		t.Fatalf("NotReady node: %v", err)
	}

	pods := ref.Pods
	bumped := pods["kube-system/cilium-abc12"]
	bumped.Restarts++
	after := k8s.Sample{Node: ref.Node, Pods: map[string]k8s.Pod{}}
	for k, v := range pods {
		after.Pods[k] = v
	}
	after.Pods["kube-system/cilium-abc12"] = bumped
	if err := k8s.Judge(after, k8s.HealthOptions{Reference: &ref}); !errors.Is(err, k8s.ErrUnhealthy) || !strings.Contains(err.Error(), "restarted 1 time") {
		t.Fatalf("restart since reference: %v", err)
	}
	if err := k8s.Judge(after, k8s.HealthOptions{}); err != nil {
		t.Fatalf("without a reference a restart count is just a number: %v", err)
	}

	crash := after.Pods["default/web-7d9f-x1"]
	crash.Ready, crash.Waiting = 1, "CrashLoopBackOff"
	after.Pods["default/web-7d9f-x1"] = crash
	if err := k8s.Judge(after, k8s.HealthOptions{}); !errors.Is(err, k8s.ErrUnhealthy) || !strings.Contains(err.Error(), "default/web-7d9f-x1 CrashLoopBackOff (1/2 ready)") {
		t.Fatalf("crashlooping pod: %v", err)
	}
	if err := k8s.Judge(after, k8s.HealthOptions{IgnoreOwners: []string{"Job", "ReplicaSet"}}); err != nil {
		t.Fatalf("ignored owner: %v", err)
	}

	job := after.Pods["default/backup-29001-k7"]
	job.Phase = "Succeeded"
	if !job.Healthy() {
		t.Fatal("Succeeded is healthy")
	}

	if got := api.Writes(); len(got) != 0 {
		t.Fatalf("health checks must never write: %v", got)
	}
}

func TestMutations(t *testing.T) {
	api, client := loaded(t)
	ctx := context.Background()
	if err := client.SetUnschedulable(ctx, "ehrlitan", true); err != nil {
		t.Fatal(err)
	}
	if err := client.SetUnschedulable(ctx, "ehrlitan", false); err != nil {
		t.Fatal(err)
	}
	if p := api.Patches["/api/v1/nodes/ehrlitan"]; len(p) != 2 || p[0] != `{"spec":{"unschedulable":true}}` || p[1] != `{"spec":{"unschedulable":false}}` {
		t.Fatalf("patches: %v", p)
	}

	api.Refuse["default/web-7d9f-x1"] = 1
	err := client.Evict(ctx, "default", "web-7d9f-x1")
	var se *kubeadm.StatusError
	if !errors.Is(err, k8s.ErrTooManyRequests) || !errors.As(err, &se) || se.RetryAfter != 1 {
		t.Fatalf("PDB refusal: %v", err)
	}
	if err := client.Evict(ctx, "default", "web-7d9f-x1"); err != nil {
		t.Fatal(err)
	}
	if api.Evictions["default/web-7d9f-x1"] != 1 {
		t.Fatalf("evictions: %v", api.Evictions)
	}

	if err := client.CreatePod(ctx, "kube-system", []byte(`{"kind":"Pod"}`)); err != nil {
		t.Fatal(err)
	}
	if len(api.Created["kube-system"]) != 1 {
		t.Fatalf("created: %v", api.Created)
	}
	if err := client.DeletePod(ctx, "kube-system", "gone"); err != nil {
		t.Fatalf("deleting an absent pod is fine: %v", err)
	}
	if _, err := client.GetPod(ctx, "kube-system", "gone"); !k8s.IsNotFound(err) {
		t.Fatalf("GetPod missing: %v", err)
	}
	if got := api.Writes(); len(got) != 6 {
		t.Fatalf("writes: %v", got)
	}
}

func TestFromKubeconfig(t *testing.T) {
	_, _, srv := fake.New(t)
	dir := t.TempDir()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), ca, 0o600); err != nil {
		t.Fatal(err)
	}
	kc := "apiVersion: v1\nkind: Config\ncurrent-context: c\nclusters:\n- name: c\n  cluster:\n    server: " + srv.URL + "\n    certificate-authority: ca.crt\ncontexts:\n- name: c\n  context:\n    cluster: c\n    user: u\nusers:\n- name: u\n  user:\n    token: t0k3n\n"
	path := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(path, []byte(kc), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := k8s.FromKubeconfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !client.Configured() || client.APIServer() != srv.URL {
		t.Fatalf("client: %s", client.APIServer())
	}
	if _, err := k8s.FromKubeconfig(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing kubeconfig must fail")
	}
}
