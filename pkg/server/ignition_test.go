package server

import (
	"encoding/json"
	"testing"

	ignitionConfig "github.com/coreos/ignition/v2/config/v3_5"
)

func TestBrigIgnitionConfigIsValid(t *testing.T) {
	raw, err := json.Marshal(brigIgnitionConfig())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	cfg, report, err := ignitionConfig.Parse(raw)
	if err != nil {
		t.Fatalf("ignition rejected brig config: %v\n%s", err, report.String())
	}
	if report.IsFatal() {
		t.Fatalf("ignition report is fatal:\n%s", report.String())
	}
	if len(cfg.Systemd.Units) != 1 {
		t.Fatalf("expected exactly one unit, got %d", len(cfg.Systemd.Units))
	}
	if cfg.Systemd.Units[0].Enabled == nil || !*cfg.Systemd.Units[0].Enabled {
		t.Fatalf("brig unit must be enabled")
	}
}
