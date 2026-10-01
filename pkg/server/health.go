package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jeefy/booty/pkg/hardware"
)

// maxHealthBodyBytes bounds a /health POST: 50 journal lines of 300
// bytes plus the rest fits in a fraction of it.
const maxHealthBodyBytes = 64 << 10

// handleHealthRequest records what booty-health.service reports once a
// node reached multi-user.target. Registered hosts only; the report is
// normalised and capped, replaces the previous one, sets running when the
// update check has not yet, and a repeated POST from the same boot (same
// bootID) converges on the same stored state. Journal lines are logged at
// debug level only: they may carry hostnames and addresses.
func handleHealthRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	raw := r.URL.Query().Get("mac")
	if raw == "" {
		writeError(w, http.StatusBadRequest, "mac query parameter is required")
		return
	}
	mac, err := hardware.NormalizeMAC(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	host, ok := hardware.Get(mac)
	if !ok {
		writeError(w, http.StatusNotFound, "host not registered")
		return
	}
	var report hardware.Health
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxHealthBodyBytes)).Decode(&report); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "health report larger than 64 KiB")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if err := report.Normalize(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	report.ReceivedAt = nowFunc().UTC().Format(time.RFC3339)
	sameBoot := host.Health.SameBoot(&report)

	updated, err := hardware.Update(mac, func(h *hardware.Host) {
		h.Health = &report
		if h.Running == "" && report.Running != "" {
			h.Running = report.Running
		}
	})
	if err != nil {
		slog.Error("Could not record health report", "mac", mac, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update host")
		return
	}
	powerSeen(mac)
	autopilotHealth(mac, &report)
	level := slog.LevelInfo
	if sameBoot {
		level = slog.LevelDebug
	}
	slog.Log(r.Context(), level, "Host health reported", "mac", mac, "running", report.Running, "failedUnits", len(report.FailedUnits), "journalErrors", len(report.JournalErrors), "firmware", report.Firmware, "kernel", report.Kernel, "bootID", report.BootID, "sameBoot", sameBoot)
	if len(report.FailedUnits) > 0 {
		slog.Log(r.Context(), level, "Host has failed units", "mac", mac, "units", report.FailedUnits)
	}
	for _, line := range report.JournalErrors {
		slog.Debug("Host journal error", "mac", mac, "line", line)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "host": updated})
}
