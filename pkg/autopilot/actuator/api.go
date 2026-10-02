package actuator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/cluster/k8s"
	"github.com/jeefy/booty/pkg/kubeadm"
)

// API reboots through the Kubernetes API: Prepare cordons the node and
// evicts every pod that is neither a DaemonSet's nor a mirror pod,
// honouring PodDisruptionBudgets by retrying 429s until DrainTimeout;
// Reboot runs a privileged reboot Pod pinned to the node from Booty's own
// image; Finish uncordons.
type API struct {
	Client       *k8s.Client
	Namespace    string
	Image        string
	DrainTimeout time.Duration
	// Sleep and Now are time.Sleep and time.Now unless a test replaces them.
	Sleep func(context.Context, time.Duration) error
	Now   func() time.Time
}

// RebootPodPrefix names reboot Pods: RebootPodPrefix + node name. The
// previous run's Pod of the same name is deleted before a new one is
// created and again by Finish once the node is back, so a Pod the reboot
// left Failed never lingers.
const RebootPodPrefix = "booty-node-reboot-"

// Labels of every reboot/poweroff Pod; IsPowerPod recognises them.
const (
	PodNameLabel       = "app.kubernetes.io/name"
	PodNameValue       = "booty"
	PodComponentLabel  = "app.kubernetes.io/component"
	PodComponentReboot = "node-reboot"
)

// IsPowerPod reports whether a pod is one of Booty's own reboot/poweroff
// Pods, by name or by label. Such a Pod legitimately ends Failed or Error:
// the node it signalled reboots underneath it, so no health check should
// count it as a workload.
func IsPowerPod(name string, labels map[string]string) bool {
	return strings.HasPrefix(name, RebootPodPrefix) || labels[PodComponentLabel] == PodComponentReboot
}

// RebootPodDeadline is activeDeadlineSeconds of a reboot Pod: the node
// reboots within seconds, the deadline only bounds a Pod that could not.
const RebootPodDeadline = 120

func (a *API) Name() string { return NameAPI }

func (a *API) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *API) sleep(ctx context.Context, d time.Duration) error {
	if a.Sleep != nil {
		return a.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Prepare cordons and drains the node.
func (a *API) Prepare(ctx context.Context, host Host) error {
	node, err := Resolve(ctx, a.Client, host)
	if err != nil {
		return err
	}
	if err := a.Client.SetUnschedulable(ctx, node.Name, true); err != nil {
		return fmt.Errorf("cordon: %w", err)
	}
	slog.Info("Node cordoned", "node", node.Name)
	return a.Drain(ctx, node.Name)
}

// Drain evicts every evictable pod on the node, retrying refusals until
// the drain timeout. DaemonSet pods and mirror (static) pods are skipped,
// as kubectl drain does with --ignore-daemonsets; finished pods too.
func (a *API) Drain(ctx context.Context, nodeName string) error {
	timeout := a.DrainTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	deadline := a.now().Add(timeout)
	pods, err := a.Client.Pods(ctx, nodeName)
	if err != nil {
		return fmt.Errorf("drain: %w", err)
	}
	var pending []k8s.Pod
	for _, p := range pods {
		if Evictable(p) {
			pending = append(pending, p)
		}
	}
	slog.Info("Draining node", "node", nodeName, "pods", len(pending), "skipped", len(pods)-len(pending), "timeout", timeout)
	wait := time.Second
	for len(pending) > 0 {
		var refused []k8s.Pod
		retryAfter := 0
		for _, p := range pending {
			err := a.Client.Evict(ctx, p.Namespace, p.Name)
			switch {
			case err == nil, k8s.IsNotFound(err):
				slog.Info("Pod evicted", "node", nodeName, "pod", p.Key())
			case errors.Is(err, k8s.ErrTooManyRequests):
				var se *kubeadm.StatusError
				if errors.As(err, &se) && se.RetryAfter > retryAfter {
					retryAfter = se.RetryAfter
				}
				refused = append(refused, p)
			default:
				return fmt.Errorf("drain: %w", err)
			}
		}
		pending = refused
		if len(pending) == 0 {
			break
		}
		if retryAfter > 0 {
			wait = time.Duration(retryAfter) * time.Second
		}
		if a.now().Add(wait).After(deadline) {
			names := make([]string, len(pending))
			for i, p := range pending {
				names[i] = p.Key()
			}
			return fmt.Errorf("drain: %d pod(s) still refused by their PodDisruptionBudget after %s: %s", len(pending), timeout, strings.Join(names, ", "))
		}
		slog.Info("Evictions refused by a PodDisruptionBudget; retrying", "node", nodeName, "pods", len(pending), "in", wait)
		if err := a.sleep(ctx, wait); err != nil {
			return fmt.Errorf("drain: %w", err)
		}
		if wait < 30*time.Second {
			wait *= 2
		}
	}
	slog.Info("Node drained", "node", nodeName)
	return nil
}

// Evictable reports whether a drain should evict the pod: not a
// DaemonSet's, not a mirror pod, not already finished.
func Evictable(p k8s.Pod) bool {
	if p.Mirror || p.Owner == "DaemonSet" || p.Owner == "Node" {
		return false
	}
	return p.Phase != "Succeeded" && p.Phase != "Failed"
}

// Reboot creates the reboot Pod on the node. It fails before touching the
// API when Image or Namespace are unknown.
func (a *API) Reboot(ctx context.Context, host Host) error {
	return a.runPod(ctx, host, false)
}

// PowerOff creates the same Pod with `node-reboot --poweroff`, so the node
// powers off instead of rebooting.
func (a *API) PowerOff(ctx context.Context, host Host) error {
	return a.runPod(ctx, host, true)
}

func (a *API) runPod(ctx context.Context, host Host, poweroff bool) error {
	if a.Image == "" {
		return errors.New("reboot pod: Booty's own image is unknown; set --autopilotImage or the BOOTY_IMAGE environment variable")
	}
	if a.Namespace == "" {
		return errors.New("reboot pod: namespace is unknown; set --autopilotNamespace or the POD_NAMESPACE environment variable")
	}
	node, err := Resolve(ctx, a.Client, host)
	if err != nil {
		return err
	}
	name := RebootPodName(node.Name)
	if err := a.Client.DeletePod(ctx, a.Namespace, name); err != nil {
		return fmt.Errorf("reboot pod: removing the previous %s: %w", name, err)
	}
	manifest, err := PowerPodManifest(a.Namespace, name, node.Name, a.Image, poweroff)
	if err != nil {
		return err
	}
	if err := a.Client.CreatePod(ctx, a.Namespace, manifest); err != nil {
		return fmt.Errorf("reboot pod: %w", err)
	}
	slog.Info("Reboot pod created", "node", node.Name, "pod", a.Namespace+"/"+name, "image", a.Image, "poweroff", poweroff)
	return nil
}

// Finish uncordons the node and removes its reboot Pod, which the reboot
// left Failed.
func (a *API) Finish(ctx context.Context, host Host) error {
	node, err := Resolve(ctx, a.Client, host)
	if err != nil {
		return err
	}
	if err := a.Client.SetUnschedulable(ctx, node.Name, false); err != nil {
		return fmt.Errorf("uncordon: %w", err)
	}
	slog.Info("Node uncordoned", "node", node.Name)
	if a.Namespace == "" {
		return nil
	}
	name := RebootPodName(node.Name)
	if err := a.Client.DeletePod(ctx, a.Namespace, name); err != nil {
		return fmt.Errorf("reboot pod: removing %s: %w", name, err)
	}
	slog.Info("Reboot pod removed", "node", node.Name, "pod", a.Namespace+"/"+name)
	return nil
}

// RebootPodName is the deterministic name of a node's reboot Pod.
func RebootPodName(nodeName string) string {
	return RebootPodPrefix + strings.ToLower(nodeName)
}

// RebootPodManifest builds the v1 Pod that reboots nodeName: Booty's own
// image running `booty node-reboot`, hostPID and privileged so it can
// signal the host's PID 1, pinned with nodeName, tolerating every taint,
// never restarted, bounded by RebootPodDeadline.
func RebootPodManifest(namespace, name, nodeName, image string) ([]byte, error) {
	return PowerPodManifest(namespace, name, nodeName, image, false)
}

// PowerPodManifest is RebootPodManifest with the choice of `--poweroff`.
func PowerPodManifest(namespace, name, nodeName, image string, poweroff bool) ([]byte, error) {
	privileged := true
	command := []string{"/booty", "node-reboot"}
	if poweroff {
		command = append(command, "--poweroff")
	}
	pod := map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"labels":    map[string]string{PodNameLabel: PodNameValue, PodComponentLabel: PodComponentReboot, "booty.jeefy.dev/node": nodeName},
		},
		"spec": map[string]any{
			"nodeName":                      nodeName,
			"hostPID":                       true,
			"restartPolicy":                 "Never",
			"activeDeadlineSeconds":         RebootPodDeadline,
			"terminationGracePeriodSeconds": 0,
			"automountServiceAccountToken":  false,
			"enableServiceLinks":            false,
			"tolerations":                   []map[string]any{{"operator": "Exists"}},
			"containers": []map[string]any{{
				"name":            "node-reboot",
				"image":           image,
				"imagePullPolicy": "IfNotPresent",
				"command":         command,
				"securityContext": map[string]any{"privileged": &privileged},
			}},
		},
	}
	return json.Marshal(pod)
}
