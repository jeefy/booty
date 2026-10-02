package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/autopilot"
	"github.com/jeefy/booty/pkg/autopilot/controller"
	"github.com/jeefy/booty/pkg/autopilot/report"
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
// dryRun, os, hosts, events, reports}), GET /autopilot/reports/<os>-<version>.md
// (the rendered, redacted report; .json for its twin), POST
// /autopilot/{os}/release/{version}/clear, which un-quarantines a release,
// and POST /autopilot/host/{mac}/clear, which ends a host's episode. Like
// /register, the writes have no authentication beyond reaching the port.
func handleAutopilotRequest(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, controller.ReportsPath) {
		handleAutopilotReport(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, autopilotClearPrefix+"host/") {
		handleAutopilotHostClear(w, r)
		return
	}
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

// handleAutopilotReport serves one rendered report from
// --dataDir/autopilot/reports/. The name is validated against the report
// key shape, so nothing outside that directory can be named.
func handleAutopilotReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, controller.ReportsPath)
	ext := path.Ext(name)
	key := strings.TrimSuffix(name, ext)
	contentType := ""
	switch ext {
	case ".md":
		contentType = "text/markdown; charset=utf-8"
	case ".json":
		contentType = "application/json"
	}
	if contentType == "" || !report.ValidKey(key) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	data, err := os.ReadFile(config.AutopilotPath(config.AutopilotReportsDir, key+ext))
	if err != nil {
		writeError(w, http.StatusNotFound, "no such report")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(data); err != nil {
		slog.Debug("Writing autopilot report failed", "key", key, "error", err)
	}
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

func handleAutopilotHostClear(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, autopilotClearPrefix+"host/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "clear" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	mac, err := hardware.NormalizeMAC(parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c := autopilotController()
	if c == nil {
		writeError(w, http.StatusConflict, "autopilot is off")
		return
	}
	switch err := c.ClearHost(mac); {
	case errors.Is(err, controller.ErrUnknownHost):
		writeError(w, http.StatusNotFound, "host not registered")
		return
	case err != nil:
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	slog.Info("Autopilot host episode cleared via API", "mac", mac, "remote", r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "mac": mac})
}
