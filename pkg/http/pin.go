package http

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/versions"
	"github.com/spf13/viper"
)

type pinRequest struct {
	Version string `json:"version"`
}

// handleFlatcarPinRequest exposes the Flatcar version pin to the Web UI.
//
//	GET  /flatcar/pin -> returns the current pin state
//	POST /flatcar/pin -> sets (or, with an empty version, clears) the pin and
//	                     triggers an asynchronous version check
func handleFlatcarPinRequest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		writePinResponse(w)
	case http.MethodPost:
		var req pinRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(fmt.Sprintf(`{"error":"invalid request body: %s"}`, err.Error())))
			return
		}

		pin := strings.TrimSpace(req.Version)
		if err := config.SetFlatcarPin(pin); err != nil {
			slog.Error("Failed to persist Flatcar version pin", "version", pin, "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(fmt.Sprintf(`{"error":"failed to save pin: %s"}`, err.Error())))
			return
		}

		if pin == "" {
			slog.Info("Flatcar version pin cleared via Web UI")
		} else {
			slog.Info("Flatcar version pinned via Web UI", "version", pin)
		}

		// Apply the change in the background so the UI gets a fast response;
		// the download/version logic gates on a successful download anyway.
		go versions.FlatcarVersionCheck()

		writePinResponse(w)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte(`{"error":"method not allowed"}`))
	}
}

func writePinResponse(w http.ResponseWriter) {
	pinned := viper.GetString(config.FlatcarVersion)
	w.Write([]byte(fmt.Sprintf(
		`{"pinned":%t,"version":"%s","current":"%s"}`,
		pinned != "", pinned, viper.GetString(config.CurrentFlatcarVersion),
	)))
}
