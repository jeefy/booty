package server

import (
	"context"
	"net/http"
	"time"

	"github.com/jeefy/booty/pkg/autopilot"
	"github.com/jeefy/booty/pkg/config"
)

var pilot *autopilot.Autopilot

func setAutopilot(a *autopilot.Autopilot) {
	pilot = a
}

// handleAutopilotRequest is GET /autopilot. In P2 it is the dry run:
// {mode, cluster:{reachable, kured, nodes}, actuator, dryRun:true}, read
// from the cluster with GETs only.
func handleAutopilotRequest(w http.ResponseWriter, r *http.Request) {
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
