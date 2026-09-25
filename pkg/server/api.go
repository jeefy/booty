package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/state"
	"github.com/jeefy/booty/pkg/versions"
	"github.com/spf13/viper"
)

const maxBodyBytes = 1 << 20

func decodeJSONBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func validateHost(h *hardware.Host) error {
	mac, err := hardware.NormalizeMAC(h.MAC)
	if err != nil {
		return err
	}
	h.MAC = mac
	if h.OS != "" && !hardware.IsValidOS(h.OS) {
		return fmt.Errorf("invalid os %q: must be one of flatcar, coreos, ublue", h.OS)
	}
	if h.IgnitionFile != "" {
		clean, err := config.CleanRelPath(h.IgnitionFile)
		if err != nil {
			return fmt.Errorf("invalid ignitionFile: %w", err)
		}
		h.IgnitionFile = clean
	}
	return nil
}

func handleRegistrationRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var h hardware.Host
	if !decodeJSONBody(w, r, &h) {
		return
	}
	if err := validateHost(&h); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	saved, err := hardware.Put(h)
	if err != nil {
		slog.Error("Registering host failed", "mac", h.MAC, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save host")
		return
	}
	slog.Info("Host registered", "mac", saved.MAC, "hostname", saved.Hostname, "os", saved.OS)

	if saved.OSTreeImage != "" {
		image := saved.OSTreeImage
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			if _, err := versions.OSTreeImagePull(ctx, image); err != nil {
				slog.Error("Error pulling OCI image", "image", image, "error", err)
			}
		}()
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "host": saved})
}

func handleUnregistrationRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		MAC string `json:"mac"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	mac, err := hardware.NormalizeMAC(req.MAC)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch err := hardware.Delete(mac); {
	case errors.Is(err, hardware.ErrNotFound):
		writeError(w, http.StatusNotFound, "host not registered")
	case err != nil:
		slog.Error("Unregistering host failed", "mac", mac, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete host")
	default:
		slog.Info("Host unregistered", "mac", mac)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func handleDataRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, hardware.Snapshot())
}

func handleHostsRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
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
	writeJSON(w, http.StatusOK, host)
}

func handleVersionRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	flatcar, coreos := state.CurrentFlatcarVersion(), state.CurrentCoreOSVersion()
	if r.URL.Path == "/version.json" {
		writeJSON(w, http.StatusOK, map[string]string{"flatcar": flatcar, "coreos": coreos})
		return
	}
	writeText(w, http.StatusOK, fmt.Sprintf("FLATCAR_VERSION=%s\nCOREOS_VERSION=%s\n", flatcar, coreos))
}

type infoResponse struct {
	Flatcar struct {
		Version       string `json:"version"`
		PinnedVersion string `json:"pinnedVersion"`
	} `json:"flatcar"`
	CoreOS struct {
		Version string `json:"version"`
	} `json:"coreos"`
	Booty struct {
		Version   string `json:"version"`
		Timestamp string `json:"timestamp"`
	} `json:"booty"`
}

func handleInfoRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var info infoResponse
	info.Flatcar.Version = state.CurrentFlatcarVersion()
	info.Flatcar.PinnedVersion = state.FlatcarPin()
	info.CoreOS.Version = state.CurrentCoreOSVersion()
	info.Booty.Version = viper.GetString(config.Version)
	info.Booty.Timestamp = viper.GetString(config.Timestamp)
	writeJSON(w, http.StatusOK, info)
}

var flatcarVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

type pinResponse struct {
	Pinned  bool   `json:"pinned"`
	Version string `json:"version"`
	Current string `json:"current"`
}

func currentPin() pinResponse {
	pin := state.FlatcarPin()
	return pinResponse{Pinned: pin != "", Version: pin, Current: state.CurrentFlatcarVersion()}
}

func handleFlatcarPinRequest(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, currentPin())
	case http.MethodPost:
		var req struct {
			Version string `json:"version"`
		}
		if !decodeJSONBody(w, r, &req) {
			return
		}
		pin := strings.TrimSpace(req.Version)
		if pin != "" && !flatcarVersionRe.MatchString(pin) {
			writeError(w, http.StatusBadRequest, "version must look like 3815.2.0 (or be empty to clear the pin)")
			return
		}
		if err := state.SetFlatcarPin(pin); err != nil {
			slog.Error("Failed to persist Flatcar version pin", "version", pin, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to save pin")
			return
		}
		if pin == "" {
			slog.Info("Flatcar version pin cleared via API")
		} else {
			slog.Info("Flatcar version pinned via API", "version", pin)
		}
		go versions.FlatcarVersionCheck()
		writeJSON(w, http.StatusOK, currentPin())
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func handleRegistryRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	images, err := versions.ListCachedImages(r.Context())
	if err != nil {
		var lre *versions.LocalRegistryError
		if errors.As(err, &lre) {
			slog.Error("Local registry query failed", "error", err)
			writeError(w, http.StatusBadGateway, "could not query local registry")
			return
		}
		slog.Error("Listing cached images failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list cached images")
		return
	}
	writeJSON(w, http.StatusOK, images)
}
