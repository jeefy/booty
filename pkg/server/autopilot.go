package server

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/autopilot"
	"github.com/jeefy/booty/pkg/autopilot/controller"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
)

var pilot *autopilot.Autopilot

func setAutopilot(a *autopilot.Autopilot) {
	pilot = a
}

func autopilotController() *controller.Controller {
	if pilot == nil {
		return nil
	}
	return pilot.Controller
}

// The boot and health handlers call these; with the mode off (no
// controller) they are no-ops, so nothing changes versus today.

func autopilotFetch(mac, kind string) {
	if c := autopilotController(); c != nil && mac != "" {
		c.ObserveFetch(mac, kind)
	}
}

func autopilotBooted(mac, running string) {
	if c := autopilotController(); c != nil {
		c.ObserveBooted(mac, running)
	}
}

func autopilotHealth(mac string, report *hardware.Health) {
	if c := autopilotController(); c != nil {
		c.ObserveHealth(mac, report)
	}
}

// autopilotRebootWanted is the kured actuator: while the controller waits
// for a host to reboot into its target, /update-check says so even when
// running == target (attempt 2 into the same release).
func autopilotRebootWanted(mac string) (bool, string) {
	if c := autopilotController(); c != nil {
		return c.RebootWanted(mac)
	}
	return false, ""
}

const autopilotClearPrefix = "/autopilot/"

// handleAutopilotRequest is GET /autopilot ({mode, cluster, actuator,
// dryRun, os, hosts, events, reports}) and POST
// /autopilot/{os}/release/{version}/clear, which un-quarantines a release.
// Like /register, the write has no authentication beyond reaching the
// port.
func handleAutopilotRequest(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, autopilotClearPrefix) {
		handleAutopilotClear(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	a := pilot
	if a == nil {
		a = &autopilot.Autopilot{Settings: autopilot.Settings{Mode: config.AutopilotOff}}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, a.Status(ctx))
}

func handleAutopilotClear(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, autopilotClearPrefix), "/")
	if len(parts) != 4 || parts[1] != "release" || parts[3] != "clear" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	osName, version := parts[0], parts[2]
	if !hardware.IsValidOS(osName) {
		writeError(w, http.StatusBadRequest, "invalid os "+osName+": must be one of "+hardware.ValidOSList())
		return
	}
	if err := hardware.ValidateTargetVersion(version); err != nil || version == "" {
		writeError(w, http.StatusBadRequest, "invalid release version")
		return
	}
	c := autopilotController()
	if c == nil {
		writeError(w, http.StatusConflict, "autopilot is off")
		return
	}
	if err := c.Clear(osName, version); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	slog.Info("Autopilot release cleared via API", "os", osName, "version", version, "remote", r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "os": osName, "version": version})
}
