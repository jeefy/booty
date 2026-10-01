package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/power"
)

var powerTracker *power.Tracker

func setPowerTracker(t *power.Tracker) {
	powerTracker = t
}

// The boot, booted, health and update-check handlers call these; without
// a tracker (tests that never set one) they are no-ops.

func powerFetch(mac, kind string) {
	if t := powerTracker; t != nil && mac != "" {
		t.ObserveFetch(mac, kind)
	}
}

func powerBooted(mac string) {
	if t := powerTracker; t != nil {
		t.ObserveBooted(mac)
	}
}

func powerSeen(mac string) {
	if t := powerTracker; t != nil {
		t.ObserveSeen(mac)
	}
}

func powerSummary() power.Summary {
	return power.Summarize(power.LiveFleet{}.Hosts())
}

const powerPrefix = "/power/"

// handlePowerRequest is GET /power (every host's power block, the power
// events, the capabilities and the summary) and POST
// /power/{mac}/{on|reboot|shutdown|cancel} with an optional JSON body
// {reason, force, drain}. Like /register, the writes have no
// authentication beyond reaching the port (plan 2026-09-30-power).
func handlePowerRequest(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, powerPrefix) {
		handlePowerAction(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	t := powerTracker
	if t == nil {
		writeJSON(w, http.StatusOK, power.Status{Hosts: map[string]*hardware.HostPower{}, Events: []power.Event{}, Summary: powerSummary()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), clusterProbeTimeout)
	defer cancel()
	writeJSON(w, http.StatusOK, t.Status(ctx))
}

func handlePowerAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, powerPrefix), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
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
	var req power.Request
	if r.ContentLength != 0 && r.Body != nil {
		if !decodeJSONBody(w, r, &req) {
			return
		}
	}
	t := powerTracker
	if t == nil {
		writeError(w, http.StatusConflict, "power tracker is not running")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	by := remoteIP(r)
	var p *hardware.HostPower
	status := http.StatusAccepted
	switch action := parts[1]; action {
	case hardware.PowerRequestOn:
		p, err = t.PowerOn(ctx, mac, by, req)
	case hardware.PowerRequestReboot:
		p, err = t.Reboot(ctx, mac, by, req)
	case hardware.PowerRequestShutdown:
		p, err = t.Shutdown(ctx, mac, by, req)
	case "cancel":
		p, err = t.Cancel(mac)
		status = http.StatusOK
	default:
		writeError(w, http.StatusNotFound, "action must be on, reboot, shutdown or cancel")
		return
	}
	switch {
	case errors.Is(err, hardware.ErrNotFound):
		writeError(w, http.StatusNotFound, "host not registered")
		return
	case err != nil && power.Conflict(err) != "":
		slog.Info("Power action refused", "mac", mac, "action", parts[1], "remote", r.RemoteAddr, "why", power.Conflict(err))
		writeJSON(w, http.StatusConflict, map[string]any{"error": power.Conflict(err), "power": hostPower(mac)})
		return
	case err != nil:
		slog.Error("Power action failed", "mac", mac, "action", parts[1], "error", err)
		writeError(w, http.StatusInternalServerError, "power action failed")
		return
	}
	slog.Info("Power action accepted", "mac", mac, "action", parts[1], "remote", r.RemoteAddr, "state", p.State, "force", req.Force)
	writeJSON(w, status, map[string]any{"status": "ok", "mac": mac, "power": p})
}

func hostPower(mac string) *hardware.HostPower {
	h, ok := hardware.Get(mac)
	if !ok || h.Power == nil {
		return &hardware.HostPower{State: hardware.PowerUnknown}
	}
	return h.Power
}
