package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/autopilot"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/power"
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
	if h.Hostname != "" {
		if err := hardware.ValidateHostname(h.Hostname); err != nil {
			return err
		}
	}
	if h.OS != "" && !hardware.IsValidOS(h.OS) {
		return fmt.Errorf("invalid os %q: must be one of %s", h.OS, hardware.ValidOSList())
	}
	if err := versions.ValidateHostOS(h); err != nil {
		return err
	}
	h.InstallDisk = strings.TrimSpace(h.InstallDisk)
	if err := hardware.ValidateInstallDisk(h.InstallDisk); err != nil {
		return err
	}
	h.Role = strings.TrimSpace(h.Role)
	if err := hardware.ValidateRole(h.Role); err != nil {
		return err
	}
	if err := hardware.ValidateBluefinFields(h); err != nil {
		return err
	}
	h.TargetVersion = strings.TrimSpace(h.TargetVersion)
	if err := versions.ValidateTargetVersion(h); err != nil {
		return err
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
	if existing, ok := hardware.Get(h.MAC); ok {
		h.Autopilot, h.Power = existing.Autopilot, existing.Power
	} else {
		h.Autopilot, h.Power = nil, nil
	}

	saved, err := hardware.Put(h)
	if err != nil {
		slog.Error("Registering host failed", "mac", h.MAC, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save host")
		return
	}
	slog.Info("Host registered", "mac", saved.MAC, "hostname", saved.Hostname, "os", saved.OS, "role", saved.Role, "targetVersion", saved.TargetVersion)
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

// handleBootedRequest is the install-complete callback POSTed by the
// installed system (see examples/*.but). It is the only place doInstall is
// cleared when --doInstallClearOn=booted; a Bluefin host that was
// installing becomes mode installed.
func handleBootedRequest(w http.ResponseWriter, r *http.Request) {
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
	ip := remoteIP(r)
	host, err := hardware.Update(mac, func(h *hardware.Host) {
		h.Booted = time.Now().UTC().Format(time.RFC3339)
		if ip != "" {
			h.IP = ip
		}
		if h.OS == "bluefin" && h.DoInstall {
			h.Mode = hardware.ModeInstalled
		}
		h.DoInstall = false
		h.InstallServedAt = ""
	})
	switch {
	case errors.Is(err, hardware.ErrNotFound):
		writeError(w, http.StatusNotFound, "host not registered")
		return
	case err != nil:
		slog.Error("Recording install completion failed", "mac", mac, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update host")
		return
	}
	powerBooted(mac)
	autopilotBooted(mac, strings.TrimSpace(r.URL.Query().Get("running")))
	slog.Info("Host reported install complete", "mac", mac, "ip", ip)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "host": host})
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
	flatcar, coreos, bluefin := trackedVersion(versions.OSFlatcar), trackedVersion(versions.OSCoreOS), trackedVersion(versions.OSBluefin)
	if r.URL.Path == "/version.json" {
		writeJSON(w, http.StatusOK, map[string]string{"flatcar": flatcar, "coreos": coreos, "bluefin": bluefin})
		return
	}
	writeText(w, http.StatusOK, fmt.Sprintf("FLATCAR_VERSION=%s\nCOREOS_VERSION=%s\nBLUEFIN_VERSION=%s\n", flatcar, coreos, bluefin))
}

// trackedVersion is the recorded current version of osName, "" for an OS
// Booty does not track (--coreOSChannel=none) whatever state remembers.
func trackedVersion(osName string) string {
	if !versions.OSTracked(osName) {
		return ""
	}
	switch osName {
	case versions.OSFlatcar:
		return state.CurrentFlatcarVersion()
	case versions.OSCoreOS:
		return state.CurrentCoreOSVersion()
	case versions.OSBluefin:
		return state.CurrentBluefinVersion()
	}
	return ""
}

type infoResponse struct {
	Flatcar struct {
		Version       string `json:"version"`
		PinnedVersion string `json:"pinnedVersion"`
	} `json:"flatcar"`
	CoreOS struct {
		Version string `json:"version"`
	} `json:"coreos"`
	Bluefin struct {
		Version       string `json:"version"`
		PinnedVersion string `json:"pinnedVersion"`
	} `json:"bluefin"`
	Booty struct {
		Version   string `json:"version"`
		Timestamp string `json:"timestamp"`
	} `json:"booty"`
	Fleet struct {
		Hosts          int `json:"hosts"`
		PendingReboots int `json:"pendingReboots"`
	} `json:"fleet"`
	// Targets is the release hosts of each OS boot and are compared
	// against in /update-check (versions.FleetTarget): current, or the
	// release the autopilot holds the fleet at; "" while none is cached.
	// The UI compares a host's running version with it.
	Targets    fleetTargets      `json:"targets"`
	SecureBoot secureBootInfo    `json:"secureBoot"`
	Autopilot  autopilot.Summary `json:"autopilot"`
	Power      power.Summary     `json:"power"`
}

type fleetTargets struct {
	Flatcar string `json:"flatcar"`
	CoreOS  string `json:"coreos"`
	Bluefin string `json:"bluefin"`
}

func currentFleetTargets() fleetTargets {
	return fleetTargets{
		Flatcar: versions.FleetTarget(versions.OSFlatcar),
		CoreOS:  versions.FleetTarget(versions.OSCoreOS),
		Bluefin: versions.FleetTarget(versions.OSBluefin),
	}
}

// secureBootInfo is the /info view of the UEFI HTTP Boot support: whether
// it is on, which artefact bundle is served and complete, the Flatcar CA
// users must enroll for Flatcar/Bluefin kernels, and which CAs the
// operator asserted the fleet's firmware trusts.
type secureBootInfo struct {
	Enabled       bool           `json:"enabled"`
	Ready         bool           `json:"ready"`
	BundleVersion string         `json:"bundleVersion"`
	Trusted       []string       `json:"trusted"`
	BootURL       string         `json:"bootURL"`
	FlatcarCA     *flatcarCAInfo `json:"flatcarCA"`
	// Warnings names the hosts seen through the Secure Boot path whose OS
	// cannot boot that way (see secureBootWarnings).
	Warnings []string `json:"warnings"`
}

type flatcarCAInfo struct {
	FlatcarVersion string `json:"flatcarVersion"`
	Sha256         string `json:"sha256"`
	Subject        string `json:"subject"`
	NotAfter       string `json:"notAfter"`
	URL            string `json:"url"`
}

func currentSecureBootInfo(hosts map[string]*hardware.Host) secureBootInfo {
	info := secureBootInfo{Enabled: viper.GetBool(config.SecureBoot), Trusted: secureBootTrusted(), Warnings: secureBootWarnings(hosts)}
	if !info.Enabled {
		return info
	}
	info.BundleVersion = versions.SecureBootBundleVersion()
	info.Ready = versions.SecureBootReady()
	info.BootURL = config.SecureBootURL()
	if ca, ok := versions.CurrentFlatcarCA(); ok {
		info.FlatcarCA = &flatcarCAInfo{FlatcarVersion: ca.FlatcarVersion, Sha256: ca.Sha256, Subject: ca.Subject, NotAfter: ca.NotAfter, URL: "http://" + config.ServerHostPort() + "/boot/secureboot/" + config.SecureBootFlatcarCADER}
	}
	return info
}

func handleInfoRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var info infoResponse
	info.Flatcar.Version = trackedVersion(versions.OSFlatcar)
	info.Flatcar.PinnedVersion = state.FlatcarPin()
	info.CoreOS.Version = trackedVersion(versions.OSCoreOS)
	info.Bluefin.Version = trackedVersion(versions.OSBluefin)
	info.Bluefin.PinnedVersion = state.BluefinPin()
	info.Booty.Version = viper.GetString(config.Version)
	info.Booty.Timestamp = viper.GetString(config.Timestamp)
	hosts := hardware.Snapshot().Hosts
	info.Fleet.Hosts = len(hosts)
	for _, h := range hosts {
		if h.RebootPending {
			info.Fleet.PendingReboots++
		}
	}
	info.Targets = currentFleetTargets()
	info.SecureBoot = currentSecureBootInfo(hosts)
	info.Autopilot = pilot.Summary()
	info.Power = powerSummary()
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

func handleStorageRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, versions.CurrentStorage())
}
