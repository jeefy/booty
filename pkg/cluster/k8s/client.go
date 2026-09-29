// Package k8s is the autopilot's view of the cluster Booty's hosts belong
// to: nodes, the pods on them and the DaemonSets, read through the same
// plain net/http client the token minter uses (kubeadm.Client), plus the
// four mutations the API actuator needs (cordon, evict, create and delete
// a pod). Nothing here decides anything; the health gate primitive
// NodeHealthy is a pure function of two samples.
package k8s

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/kubeadm"
)

// Typed failures of every call, re-exported from kubeadm so callers need
// not import both: errors.Is(err, ErrUnreachable) for a dead API server,
// ErrForbidden for missing RBAC, ErrNotFound for a node or pod that is
// not there, ErrNotInCluster when no API server is configured at all.
var (
	ErrUnreachable  = kubeadm.ErrUnreachable
	ErrForbidden    = kubeadm.ErrForbidden
	ErrNotFound     = kubeadm.ErrNotFound
	ErrNotInCluster = kubeadm.ErrNotInCluster
)

const (
	listLimit = 32 << 20
	itemLimit = 1 << 20
)

// Client reads and, for the actuator, writes cluster objects.
type Client struct {
	api *kubeadm.Client
}

// New wraps an existing API client (the minter's, so both share one
// transport).
func New(api *kubeadm.Client) *Client {
	return &Client{api: api}
}

// FromConfig builds a Client from a KubeConfig.
func FromConfig(cfg kubeadm.KubeConfig) *Client {
	return New(kubeadm.NewClient(cfg))
}

// FromKubeconfig builds a Client from the current context of the
// kubeconfig file at path.
func FromKubeconfig(path string) (*Client, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading kubeconfig: %w", err)
	}
	cfg, err := kubeadm.ParseKubeconfig(data, filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("kubeconfig %s: %w", path, err)
	}
	return FromConfig(cfg), nil
}

// InCluster builds a Client from the in-cluster environment. Outside a
// cluster it is not nil but every call fails with ErrNotInCluster; check
// Configured.
func InCluster() *Client {
	return FromConfig(kubeadm.InClusterConfig())
}

// Configured reports whether the client knows an API server at all.
func (c *Client) Configured() bool { return c != nil && c.api.APIServer() != "" }

// APIServer is the URL the client talks to.
func (c *Client) APIServer() string {
	if c == nil {
		return ""
	}
	return c.api.APIServer()
}

// API exposes the underlying client for callers that need raw access.
func (c *Client) API() *kubeadm.Client { return c.api }

// Node is what the autopilot needs to know about a cluster node.
type Node struct {
	Name          string            `json:"name"`
	Ready         bool              `json:"ready"`
	ReadyReason   string            `json:"readyReason,omitempty"`
	ReadySince    time.Time         `json:"readySince"`
	Unschedulable bool              `json:"unschedulable"`
	ControlPlane  bool              `json:"controlPlane"`
	Labels        map[string]string `json:"labels,omitempty"`
	NodeInfo      NodeInfo          `json:"nodeInfo"`
	// Kured holds the node's weave.works/kured-* and kured.dev/*
	// annotations (lock, reboot-in-progress, most-recent-reboot-needed).
	Kured map[string]string `json:"kured,omitempty"`
}

// NodeInfo is status.nodeInfo, the fields the health gate and the reports
// compare against the release the node was meant to boot.
type NodeInfo struct {
	SystemUUID              string `json:"systemUUID"`
	KubeletVersion          string `json:"kubeletVersion"`
	OSImage                 string `json:"osImage"`
	KernelVersion           string `json:"kernelVersion"`
	ContainerRuntimeVersion string `json:"containerRuntimeVersion"`
}

// Pod is a pod on a node as the health gate sees it.
type Pod struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	NodeName  string `json:"nodeName"`
	Phase     string `json:"phase"`
	// Owner is the kind of the controlling ownerReference (DaemonSet,
	// ReplicaSet, Job, StatefulSet, Node for mirror pods), empty for bare
	// pods.
	Owner      string `json:"owner,omitempty"`
	OwnerName  string `json:"ownerName,omitempty"`
	Mirror     bool   `json:"mirror,omitempty"`
	Containers int    `json:"containers"`
	Ready      int    `json:"ready"`
	Restarts   int    `json:"restarts"`
	// Waiting is the waiting reason of the first container not running
	// (CrashLoopBackOff, ImagePullBackOff, ...), empty when all run.
	Waiting string `json:"waiting,omitempty"`
}

// Key is namespace/name.
func (p Pod) Key() string { return p.Namespace + "/" + p.Name }

// Healthy reports whether the pod is Running with every container ready,
// or has finished (Succeeded).
func (p Pod) Healthy() bool {
	return p.Phase == "Succeeded" || (p.Phase == "Running" && p.Ready == p.Containers)
}

// DaemonSet is the identity of a DaemonSet, enough for kured detection.
type DaemonSet struct {
	Namespace string            `json:"namespace"`
	Name      string            `json:"name"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// IsKured reports whether the DaemonSet is a kured deployment: named
// kured, or labelled app=kured or app.kubernetes.io/name=kured.
func (d DaemonSet) IsKured() bool {
	return d.Name == "kured" || d.Labels["app"] == "kured" || d.Labels["app.kubernetes.io/name"] == "kured"
}

type objectMeta struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	Labels          map[string]string `json:"labels"`
	Annotations     map[string]string `json:"annotations"`
	OwnerReferences []struct {
		Kind       string `json:"kind"`
		Name       string `json:"name"`
		Controller bool   `json:"controller"`
	} `json:"ownerReferences"`
}

type nodeObject struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		Unschedulable bool `json:"unschedulable"`
	} `json:"spec"`
	Status struct {
		Conditions []struct {
			Type               string    `json:"type"`
			Status             string    `json:"status"`
			Reason             string    `json:"reason"`
			LastTransitionTime time.Time `json:"lastTransitionTime"`
		} `json:"conditions"`
		NodeInfo NodeInfo `json:"nodeInfo"`
	} `json:"status"`
}

func (n nodeObject) toNode() Node {
	node := Node{
		Name:          n.Metadata.Name,
		Unschedulable: n.Spec.Unschedulable,
		Labels:        n.Metadata.Labels,
		NodeInfo:      n.Status.NodeInfo,
	}
	_, cp := n.Metadata.Labels["node-role.kubernetes.io/control-plane"]
	_, master := n.Metadata.Labels["node-role.kubernetes.io/master"]
	node.ControlPlane = cp || master
	for _, c := range n.Status.Conditions {
		if c.Type == "Ready" {
			node.Ready = c.Status == "True"
			node.ReadyReason = c.Reason
			node.ReadySince = c.LastTransitionTime
		}
	}
	for k, v := range n.Metadata.Annotations {
		if strings.HasPrefix(k, "weave.works/kured-") || strings.HasPrefix(k, "kured.dev/") {
			if node.Kured == nil {
				node.Kured = map[string]string{}
			}
			node.Kured[k] = v
		}
	}
	return node
}

type podObject struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		NodeName   string `json:"nodeName"`
		Containers []struct {
			Name string `json:"name"`
		} `json:"containers"`
	} `json:"spec"`
	Status struct {
		Phase             string `json:"phase"`
		ContainerStatuses []struct {
			Ready        bool `json:"ready"`
			RestartCount int  `json:"restartCount"`
			State        struct {
				Waiting *struct {
					Reason string `json:"reason"`
				} `json:"waiting"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

func (p podObject) toPod() Pod {
	pod := Pod{
		Namespace:  p.Metadata.Namespace,
		Name:       p.Metadata.Name,
		NodeName:   p.Spec.NodeName,
		Phase:      p.Status.Phase,
		Containers: len(p.Spec.Containers),
	}
	_, pod.Mirror = p.Metadata.Annotations["kubernetes.io/config.mirror"]
	for _, o := range p.Metadata.OwnerReferences {
		if o.Controller || pod.Owner == "" {
			pod.Owner, pod.OwnerName = o.Kind, o.Name
		}
	}
	for _, c := range p.Status.ContainerStatuses {
		if c.Ready {
			pod.Ready++
		}
		pod.Restarts += c.RestartCount
		if c.State.Waiting != nil && pod.Waiting == "" {
			pod.Waiting = c.State.Waiting.Reason
		}
	}
	return pod
}

// Nodes lists every node.
func (c *Client) Nodes(ctx context.Context) ([]Node, error) {
	var list struct {
		Items []nodeObject `json:"items"`
	}
	if err := c.api.Decode(ctx, "GET", "/api/v1/nodes", nil, &list, listLimit); err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	nodes := make([]Node, 0, len(list.Items))
	for _, n := range list.Items {
		nodes = append(nodes, n.toNode())
	}
	return nodes, nil
}

// Node fetches one node by name (ErrNotFound when it does not exist).
func (c *Client) Node(ctx context.Context, name string) (Node, error) {
	var n nodeObject
	if err := c.api.Decode(ctx, "GET", "/api/v1/nodes/"+url.PathEscape(name), nil, &n, itemLimit); err != nil {
		return Node{}, fmt.Errorf("node %s: %w", name, err)
	}
	return n.toNode(), nil
}

// Pods lists the pods scheduled on nodeName across all namespaces.
func (c *Client) Pods(ctx context.Context, nodeName string) ([]Pod, error) {
	var list struct {
		Items []podObject `json:"items"`
	}
	path := "/api/v1/pods?fieldSelector=" + url.QueryEscape("spec.nodeName="+nodeName)
	if err := c.api.Decode(ctx, "GET", path, nil, &list, listLimit); err != nil {
		return nil, fmt.Errorf("listing pods on %s: %w", nodeName, err)
	}
	pods := make([]Pod, 0, len(list.Items))
	for _, p := range list.Items {
		pods = append(pods, p.toPod())
	}
	return pods, nil
}

// DaemonSets lists every DaemonSet in the cluster.
func (c *Client) DaemonSets(ctx context.Context) ([]DaemonSet, error) {
	var list struct {
		Items []struct {
			Metadata objectMeta `json:"metadata"`
		} `json:"items"`
	}
	if err := c.api.Decode(ctx, "GET", "/apis/apps/v1/daemonsets", nil, &list, listLimit); err != nil {
		return nil, fmt.Errorf("listing daemonsets: %w", err)
	}
	sets := make([]DaemonSet, 0, len(list.Items))
	for _, d := range list.Items {
		sets = append(sets, DaemonSet{Namespace: d.Metadata.Namespace, Name: d.Metadata.Name, Labels: d.Metadata.Labels})
	}
	return sets, nil
}

// KuredPresent reports whether a kured DaemonSet exists in any namespace.
func (c *Client) KuredPresent(ctx context.Context) (bool, error) {
	sets, err := c.DaemonSets(ctx)
	if err != nil {
		return false, err
	}
	for _, d := range sets {
		if d.IsKured() {
			return true, nil
		}
	}
	return false, nil
}

// Sample is one observation of a node and its pods, the unit the health
// gate compares: the baseline taken before a reboot and every later look.
type Sample struct {
	Node  Node           `json:"node"`
	Pods  map[string]Pod `json:"pods"`
	Taken time.Time      `json:"taken"`
}

// Sample reads the node and its pods.
func (c *Client) Sample(ctx context.Context, nodeName string) (Sample, error) {
	node, err := c.Node(ctx, nodeName)
	if err != nil {
		return Sample{}, err
	}
	pods, err := c.Pods(ctx, nodeName)
	if err != nil {
		return Sample{}, err
	}
	s := Sample{Node: node, Pods: make(map[string]Pod, len(pods)), Taken: time.Now()}
	for _, p := range pods {
		s.Pods[p.Key()] = p
	}
	return s, nil
}

// HealthOptions parametrise NodeHealthy. Reference is the baseline sample
// (nil: no restart comparison); pods whose owner kind is in IgnoreOwners
// (default: Job) never count against the node.
type HealthOptions struct {
	Reference    *Sample
	IgnoreOwners []string
}

// ErrUnhealthy is wrapped by every NodeHealthy failure.
var ErrUnhealthy = errors.New("node unhealthy")

// NodeHealthy is the health-gate primitive: the node is Ready, every pod on
// it that is not a Job's is Running with all containers ready (or has
// Succeeded), and no container restarted since the reference sample. It
// returns the sample it judged so callers can keep it as the next
// reference, and an error wrapping ErrUnhealthy naming the first problem.
func (c *Client) NodeHealthy(ctx context.Context, nodeName string, opts HealthOptions) (Sample, error) {
	s, err := c.Sample(ctx, nodeName)
	if err != nil {
		return Sample{}, err
	}
	return s, Judge(s, opts)
}

// Judge applies NodeHealthy's rules to an already taken sample.
func Judge(s Sample, opts HealthOptions) error {
	if !s.Node.Ready {
		return fmt.Errorf("%w: node %s not Ready (%s)", ErrUnhealthy, s.Node.Name, s.Node.ReadyReason)
	}
	ignore := opts.IgnoreOwners
	if ignore == nil {
		ignore = []string{"Job"}
	}
	for _, k := range slices.Sorted(maps.Keys(s.Pods)) {
		p := s.Pods[k]
		if slices.Contains(ignore, p.Owner) {
			continue
		}
		if !p.Healthy() {
			state := p.Phase
			if p.Waiting != "" {
				state = p.Waiting
			}
			return fmt.Errorf("%w: pod %s %s (%d/%d ready)", ErrUnhealthy, k, state, p.Ready, p.Containers)
		}
		if opts.Reference != nil {
			if ref, ok := opts.Reference.Pods[k]; ok && p.Restarts > ref.Restarts {
				return fmt.Errorf("%w: pod %s restarted %d time(s) since the reference", ErrUnhealthy, k, p.Restarts-ref.Restarts)
			}
		}
	}
	return nil
}

// Drain is kubeadm.Drain: discard a 2xx body, turn anything else into a
// *kubeadm.StatusError.
var Drain = kubeadm.Drain

// IsNotFound reports whether err is a 404 from the API server.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// IsForbidden reports whether err is a 401/403 from the API server.
func IsForbidden(err error) bool { return errors.Is(err, ErrForbidden) }

// IsUnreachable reports whether the API server could not be reached at all
// (transport failure or no API server configured).
func IsUnreachable(err error) bool {
	return errors.Is(err, ErrUnreachable) || errors.Is(err, ErrNotInCluster)
}

// ErrTooManyRequests is the eviction API's PodDisruptionBudget refusal.
var ErrTooManyRequests = kubeadm.ErrTooManyRequests
