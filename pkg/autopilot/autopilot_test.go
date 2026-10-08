package autopilot

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/autopilot/controller"
	"github.com/jeefy/booty/pkg/config"
)

func validSettings() Settings {
	return Settings{Mode: config.AutopilotGuard, DrainTimeout: time.Minute, HealthWindow: time.Minute, RetryAfter: time.Minute}
}

func TestValidateRejectsNegativeSoakAndCooldown(t *testing.T) {
	s := validSettings()
	if err := s.Validate(); err != nil {
		t.Fatalf("zero clocks are the default: %v", err)
	}
	s.Soak, s.Cooldown = 24*time.Hour, 48*time.Hour
	if err := s.Validate(); err != nil {
		t.Fatalf("positive clocks: %v", err)
	}
	for _, tc := range []struct {
		name string
		set  func(*Settings)
		flag string
	}{
		{"soak", func(s *Settings) { s.Soak = -time.Second }, config.AutopilotSoak},
		{"cooldown", func(s *Settings) { s.Cooldown = -time.Hour }, config.AutopilotCooldown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := validSettings()
			tc.set(&s)
			err := s.Validate()
			if err == nil || !strings.Contains(err.Error(), "--"+tc.flag) {
				t.Fatalf("a negative --%s must be a start-up error, got %v", tc.flag, err)
			}
			s.Mode = config.AutopilotOff
			if err := s.Validate(); err == nil {
				t.Fatal("a negative duration is a typo whatever the mode")
			}
		})
	}
}

func TestStatusNamesSoakAndCooldownOnlyWhenSet(t *testing.T) {
	newCtrl := func(t *testing.T, soak, cooldown time.Duration) *controller.Controller {
		t.Helper()
		ctrl, err := controller.New(controller.Options{Mode: config.AutopilotGuard, Fleet: controller.LiveFleet{}, StatePath: filepath.Join(t.TempDir(), "state.json"), HealthWindow: 15 * time.Minute, RetryAfter: time.Hour, Soak: soak, Cooldown: cooldown})
		if err != nil {
			t.Fatal(err)
		}
		return ctrl
	}
	fields := func(t *testing.T, a *Autopilot) map[string]any {
		t.Helper()
		raw, err := json.Marshal(a.Status(context.Background()))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	unpaced := &Autopilot{Settings: Settings{Mode: config.AutopilotGuard, HealthWindow: 15 * time.Minute, RetryAfter: time.Hour}, Controller: newCtrl(t, 0, 0)}
	m := fields(t, unpaced)
	if _, ok := m["soak"]; ok {
		t.Fatalf("soak must be omitted when zero: %v", m)
	}
	if _, ok := m["cooldown"]; ok {
		t.Fatalf("cooldown must be omitted when zero: %v", m)
	}
	if m["healthWindow"] != "15m0s" {
		t.Fatalf("healthWindow: %v", m["healthWindow"])
	}

	paced := &Autopilot{Settings: Settings{Mode: config.AutopilotGuard, HealthWindow: 15 * time.Minute, RetryAfter: time.Hour, Soak: 24 * time.Hour, Cooldown: 48 * time.Hour}, Controller: newCtrl(t, 24*time.Hour, 48*time.Hour)}
	m = fields(t, paced)
	if m["soak"] != "24h0m0s" || m["cooldown"] != "48h0m0s" {
		t.Fatalf("soak %v cooldown %v", m["soak"], m["cooldown"])
	}
	os, _ := m["os"].(map[string]any)
	flatcar, _ := os["flatcar"].(map[string]any)
	if _, ok := flatcar["canaries"]; !ok {
		t.Fatalf("per-OS canaries count: %v", flatcar)
	}
}
