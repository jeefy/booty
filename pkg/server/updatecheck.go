package server

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/state"
	"github.com/jeefy/booty/pkg/versions"
)

// updateCheckPersistInterval bounds how often an unchanged check result is
// rewritten to hardware.json; the timer fires every 10 minutes per host.
const updateCheckPersistInterval = time.Minute

var nowFunc = time.Now

var coreOSVersionRe = regexp.MustCompile(`^\d+\.\d{8}\.\d+\.\d+$`)

// Bluefin Server updates itself with systemd-sysupdate; Booty never asks it
// to reboot, and a re-PXE would only re-image the disk.
const bluefinUpdateReason = "bluefin updates itself via systemd-sysupdate; re-PXE only re-images"

type updateCheckResponse struct {
	RebootRequired bool   `json:"rebootRequired"`
	Running        string `json:"running"`
	Target         string `json:"target"`
	Reason         string `json:"reason"`
}

type updateReport struct {
	OS      string
	Version string
	Image   string
	Digest  string
}

func (r updateReport) running() string {
	if r.Image == "" {
		return r.Version
	}
	if r.Digest == "" {
		return r.Image
	}
	return r.Image + "@" + r.Digest
}

// handleUpdateCheckRequest answers the booty-update-check script. It never
// reports rebootRequired=true unless Booty positively knows the host is
// behind what it serves; the client treats non-2xx as "leave state alone".
func handleUpdateCheckRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	q := r.URL.Query()
	raw := q.Get("mac")
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
	rep := updateReport{
		OS:      strings.ToLower(strings.TrimSpace(q.Get("os"))),
		Version: strings.TrimSpace(q.Get("version")),
		Image:   normalizeImageRef(q.Get("image")),
		Digest:  strings.TrimSpace(q.Get("digest")),
	}
	resp := evaluateUpdate(host, rep)
	recordUpdateCheck(mac, host, resp)
	writeJSON(w, http.StatusOK, resp)
}

func evaluateUpdate(host *hardware.Host, rep updateReport) updateCheckResponse {
	resp := updateCheckResponse{Running: rep.running()}
	osName := rep.OS
	if osName == "" {
		osName = host.OS
	}
	switch {
	case osName == "bluefin" || host.OS == "bluefin":
		resp.Reason = bluefinUpdateReason
		return resp
	case osName == "flatcar":
		return evaluateFlatcar(rep, resp)
	case osName == "coreos" || host.OSTreeImage != "":
		return evaluateOSTree(host, rep, resp)
	}
	resp.Reason = "unknown os; cannot determine target"
	return resp
}

func evaluateFlatcar(rep updateReport, resp updateCheckResponse) updateCheckResponse {
	target := state.CurrentFlatcarVersion()
	if target == "" || target == "0.0.0" {
		resp.Reason = "server has no flatcar version yet"
		return resp
	}
	resp.Target = target
	if rep.Version == "" {
		resp.Reason = "host reported no version"
		return resp
	}
	if rep.Version == target {
		resp.Reason = "flatcar up to date"
		return resp
	}
	resp.RebootRequired = true
	resp.Reason = "flatcar " + rep.Version + " differs from served " + target
	return resp
}

func evaluateOSTree(host *hardware.Host, rep updateReport, resp updateCheckResponse) updateCheckResponse {
	targetImage := host.OSTreeImage
	if targetImage == "" {
		targetImage = rep.Image
	}
	if targetImage != "" {
		if localDigest := localImageDigest(targetImage); localDigest != "" {
			resp.Target = targetImage + "@" + localDigest
			switch rep.Digest {
			case "":
				resp.Reason = "host reported no image digest"
			case localDigest:
				resp.Reason = "image up to date"
			default:
				resp.RebootRequired = true
				resp.Reason = "running image digest differs from cached " + targetImage
			}
			return resp
		}
		if host.OSTreeImage != "" {
			resp.Target = targetImage
			resp.Reason = "target image not cached yet"
			return resp
		}
	}
	return evaluateCoreOSVersion(rep, resp)
}

func evaluateCoreOSVersion(rep updateReport, resp updateCheckResponse) updateCheckResponse {
	target := state.CurrentCoreOSVersion()
	if target == "" || target == "0.0.0" {
		resp.Reason = "server has no coreos version yet"
		return resp
	}
	resp.Target = target
	if !coreOSVersionRe.MatchString(rep.Version) {
		resp.Reason = "cannot compare version " + rep.Version + " with coreos " + target
		return resp
	}
	if rep.Version == target {
		resp.Reason = "coreos up to date"
		return resp
	}
	resp.RebootRequired = true
	resp.Reason = "coreos " + rep.Version + " differs from served " + target
	return resp
}

func localImageDigest(image string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	digest, err := digestLookup(versions.LocalImageRef(image), versions.LocalOptions(ctx)...)
	if err != nil {
		slog.Debug("Image not in local cache", "image", image, "error", err)
		return ""
	}
	return digest
}

// normalizeImageRef strips the rpm-ostree transport prefixes and Booty's own
// client-facing registry so the reference matches what hosts are registered
// with (e.g. ghcr.io/ublue-os/bazzite:stable, a Universal Blue image booted
// as os=coreos).
func normalizeImageRef(image string) string {
	image = strings.TrimSpace(image)
	for _, prefix := range []string{"ostree-unverified-registry:", "ostree-image-signed:", "ostree-unverified-image:", "ostree-remote-image:", "docker://", "registry:"} {
		image = strings.TrimPrefix(image, prefix)
	}
	if idx := strings.Index(image, ":docker://"); idx >= 0 {
		image = image[idx+len(":docker://"):]
	}
	image = strings.TrimPrefix(image, config.ClientRegistry()+"/")
	return image
}

func recordUpdateCheck(mac string, host *hardware.Host, resp updateCheckResponse) {
	now := nowFunc().UTC()
	if host.Running == resp.Running && host.RebootPending == resp.RebootRequired {
		if last, err := time.Parse(time.RFC3339, host.LastCheck); err == nil && now.Sub(last) < updateCheckPersistInterval {
			return
		}
	}
	if host.RebootPending != resp.RebootRequired {
		slog.Info("Host reboot state changed", "mac", mac, "rebootRequired", resp.RebootRequired, "running", resp.Running, "target", resp.Target, "reason", resp.Reason)
	}
	_, err := hardware.Update(mac, func(h *hardware.Host) {
		h.Running = resp.Running
		h.LastCheck = now.Format(time.RFC3339)
		h.RebootPending = resp.RebootRequired
	})
	if err != nil {
		slog.Error("Could not record update check", "mac", mac, "error", err)
	}
}
