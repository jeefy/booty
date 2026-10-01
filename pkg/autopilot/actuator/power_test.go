package actuator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/jeefy/booty/pkg/cluster/k8s"
)

func TestOperatorNeverPicksKured(t *testing.T) {
	ctx := context.Background()
	api, client := loaded(t)
	c := NewChooser(Options{Client: client, SSHKeyPath: "/k", Namespace: "kube-system", Image: "img"})
	r, err := c.Operator(ctx)
	if err != nil || r.Name() != NameSSH {
		t.Fatalf("kured present, key set: the operator actuator is SSH: %v %v", r, err)
	}
	if r.(*SSH).Drain != client {
		t.Fatal("SSH must drain through the reachable API")
	}
	if got := c.OperatorName(ctx); got != NameSSH {
		t.Fatalf("OperatorName %q", got)
	}

	c = NewChooser(Options{Client: client, Namespace: "kube-system", Image: "img"})
	r, err = c.Operator(ctx)
	if err != nil || r.Name() != NameAPI {
		t.Fatalf("kured present, no key: the operator actuator is the API, not kured: %v %v", r, err)
	}
	if got := api.Writes(); len(got) != 0 {
		t.Fatalf("choosing must never write: %v", got)
	}

	c = NewChooser(Options{})
	if _, err := c.Operator(ctx); !errors.Is(err, ErrNoOperatorActuator) {
		t.Fatalf("no client, no key: %v", err)
	}
	if got := c.OperatorName(ctx); got != NameNone {
		t.Fatalf("OperatorName %q", got)
	}

	dead := k8s.FromConfig(kubeadmConfig("https://127.0.0.1:1"))
	c = NewChooser(Options{Client: dead})
	if _, err := c.Operator(ctx); !errors.Is(err, ErrNoOperatorActuator) {
		t.Fatalf("unreachable API, no key: %v", err)
	}
	c = NewChooser(Options{Client: dead, SSHKeyPath: "/k"})
	r, err = c.Operator(ctx)
	if err != nil || r.Name() != NameSSH || r.(*SSH).Drain != nil {
		t.Fatalf("unreachable API, key set: SSH without a drain: %v %v", r, err)
	}
}

func TestPowerPodManifestCarriesPoweroff(t *testing.T) {
	for _, poweroff := range []bool{false, true} {
		raw, err := PowerPodManifest("kube-system", "booty-node-reboot-n1", "n1", "img", poweroff)
		if err != nil {
			t.Fatal(err)
		}
		var pod struct {
			Spec struct {
				Containers []struct {
					Command []string `json:"command"`
				} `json:"containers"`
			} `json:"spec"`
		}
		if err := json.Unmarshal(raw, &pod); err != nil {
			t.Fatal(err)
		}
		got := strings.Join(pod.Spec.Containers[0].Command, " ")
		want := "/booty node-reboot"
		if poweroff {
			want += " --poweroff"
		}
		if got != want {
			t.Fatalf("poweroff=%v: command %q, want %q", poweroff, got, want)
		}
	}
}

func TestSSHPowerOffCommand(t *testing.T) {
	s := &SSH{}
	if got := s.PowerOffCommand("bluefin"); got != "systemctl poweroff" {
		t.Fatalf("bluefin: %q", got)
	}
	if got := s.PowerOffCommand("flatcar"); got != "sudo systemctl poweroff" {
		t.Fatalf("flatcar: %q", got)
	}
}

func TestNodePowerOffSignalsSIGRTMINPlus4(t *testing.T) {
	if PowerOffSignal != syscall.Signal(38) {
		t.Fatalf("SIGRTMIN+4 is 38 under glibc, got %d", int(PowerOffSignal))
	}
	var got syscall.Signal
	synced := false
	if err := NodePowerOff(1, func() { synced = true }, func(_ int, sig syscall.Signal) error { got = sig; return nil }); err != nil {
		t.Fatal(err)
	}
	if !synced || got != PowerOffSignal {
		t.Fatalf("synced=%v signal=%v", synced, got)
	}
	if err := NodePowerOff(1, func() {}, func(int, syscall.Signal) error { return os.ErrPermission }); err == nil || !strings.Contains(err.Error(), "SIGRTMIN+4") {
		t.Fatalf("error must name the signal: %v", err)
	}
}
