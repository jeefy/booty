package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// The mutations below are the whole write surface of the package and only
// the API actuator (pkg/autopilot/actuator) calls them; the dry-run wiring
// of P2 and GET /autopilot never do. Keep it that way: a test in the
// server package asserts the fake API sees GETs only.

// SetUnschedulable cordons (true) or uncordons (false) the node with a
// strategic-merge patch of spec.unschedulable.
func (c *Client) SetUnschedulable(ctx context.Context, nodeName string, unschedulable bool) error {
	body, err := json.Marshal(map[string]any{"spec": map[string]any{"unschedulable": unschedulable}})
	if err != nil {
		return err
	}
	resp, err := c.api.Do(ctx, "PATCH", "/api/v1/nodes/"+url.PathEscape(nodeName), body, "application/strategic-merge-patch+json")
	if err != nil {
		return fmt.Errorf("patching node %s: %w", nodeName, err)
	}
	if err := Drain(resp); err != nil {
		return fmt.Errorf("patching node %s: %w", nodeName, err)
	}
	return nil
}

// Evict asks the eviction API to remove the pod, which honours its
// PodDisruptionBudgets: a refusal is a *kubeadm.StatusError matching
// ErrTooManyRequests (with RetryAfter set), a pod that is already gone is
// ErrNotFound.
func (c *Client) Evict(ctx context.Context, namespace, name string) error {
	body, err := json.Marshal(map[string]any{
		"apiVersion": "policy/v1",
		"kind":       "Eviction",
		"metadata":   map[string]string{"name": name, "namespace": namespace},
	})
	if err != nil {
		return err
	}
	path := "/api/v1/namespaces/" + url.PathEscape(namespace) + "/pods/" + url.PathEscape(name) + "/eviction"
	resp, err := c.api.Do(ctx, "POST", path, body)
	if err != nil {
		return fmt.Errorf("evicting %s/%s: %w", namespace, name, err)
	}
	if err := Drain(resp); err != nil {
		return fmt.Errorf("evicting %s/%s: %w", namespace, name, err)
	}
	return nil
}

// CreatePod posts a v1 Pod manifest into namespace. The manifest is the
// caller's (the actuator builds the reboot pod); this only sends it.
func (c *Client) CreatePod(ctx context.Context, namespace string, manifest []byte) error {
	resp, err := c.api.Do(ctx, "POST", "/api/v1/namespaces/"+url.PathEscape(namespace)+"/pods", manifest)
	if err != nil {
		return fmt.Errorf("creating pod in %s: %w", namespace, err)
	}
	if err := Drain(resp); err != nil {
		return fmt.Errorf("creating pod in %s: %w", namespace, err)
	}
	return nil
}

// DeletePod deletes a pod; a pod that does not exist is not an error.
func (c *Client) DeletePod(ctx context.Context, namespace, name string) error {
	resp, err := c.api.Do(ctx, "DELETE", "/api/v1/namespaces/"+url.PathEscape(namespace)+"/pods/"+url.PathEscape(name), nil)
	if err != nil {
		return fmt.Errorf("deleting %s/%s: %w", namespace, name, err)
	}
	if err := Drain(resp); err != nil && !IsNotFound(err) {
		return fmt.Errorf("deleting %s/%s: %w", namespace, name, err)
	}
	return nil
}

// GetPod fetches one pod (ErrNotFound when absent).
func (c *Client) GetPod(ctx context.Context, namespace, name string) (Pod, error) {
	var p podObject
	if err := c.api.Decode(ctx, "GET", "/api/v1/namespaces/"+url.PathEscape(namespace)+"/pods/"+url.PathEscape(name), nil, &p, itemLimit); err != nil {
		return Pod{}, fmt.Errorf("pod %s/%s: %w", namespace, name, err)
	}
	return p.toPod(), nil
}
