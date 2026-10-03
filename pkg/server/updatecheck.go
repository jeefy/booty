package server

import (
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/versions"
)

// updateCheckPersistInterval bounds how often an unchanged check result is
// rewritten to hardware.json; the timer fires every 10 minutes per host.
const updateCheckPersistInterval = time.Minute

var nowFunc = time.Now

var coreOSVersionRe = regexp.MustCompile(`^\d+\.\d{8}\.\d+\.\d+$`)

// bluefinUpdateReason is the reboot reason for a diskless Bluefin host: a
// reboot re-images it from Booty into its target release, which is the
// whole upgrade (and rollback) mechanism.
const bluefinUpdateReason = "bluefin: re-image on reboot into "

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
	if want, reason := autopilotRebootWanted(mac); want && !resp.RebootRequired && resp.Target != "" {
		resp.RebootRequired, resp.Reason = true, reason
	}
	recordUpdateCheck(mac, host, resp)
	powerSeen(mac)
	writeJSON(w, http.StatusOK, resp)
}

// evaluateUpdate answers rebootRequired = effectiveTarget(host) != running
// for every OS: the target is the host's targetVersion when set, else the
// fleet target (the OS's current release); false when no release is
// cached or the host's running version is unknown. Without a
// targetVersion the Flatcar and CoreOS answers are what they were before
// targetVersion existed.
func evaluateUpdate(host *hardware.Host, rep updateReport) updateCheckResponse {
	resp := updateCheckResponse{Running: rep.running()}
	osName := rep.OS
	if osName == "bluefin-server" {
		osName = versions.OSBluefin
	}
	if osName == "" {
		osName = host.OS
	}
	switch {
	case osName == versions.OSBluefin || host.OS == versions.OSBluefin:
		return evaluateBluefin(host, rep, resp)
	case osName == versions.OSFlatcar:
		return evaluateFlatcar(host, rep, resp)
	case osName == versions.OSCoreOS || host.OSTreeImage != "":
		return evaluateOSTree(host, rep, resp)
	}
	resp.Reason = "unknown os; cannot determine target"
	return resp
}

// hostTarget is the release host should run: its targetVersion when set
// and cached, else the fleet target of osName (which may not be the OS the
// host is registered as, when the node reports another os= than expected).
func hostTarget(host *hardware.Host, osName string) string {
	if host.TargetVersion != "" && versions.ReleaseCached(osName, host.TargetVersion) {
		return host.TargetVersion
	}
	return versions.FleetTarget(osName)
}

func pinnedSuffix(host *hardware.Host, target string) string {
	if host.TargetVersion != "" && host.TargetVersion == target {
		return " (host targetVersion)"
	}
	return ""
}

func evaluateFlatcar(host *hardware.Host, rep updateReport, resp updateCheckResponse) updateCheckResponse {
	target := hostTarget(host, versions.OSFlatcar)
	if target == "" {
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
	resp.Reason = "flatcar " + rep.Version + " differs from served " + target + pinnedSuffix(host, target)
	return resp
}

func evaluateBluefin(host *hardware.Host, rep updateReport, resp updateCheckResponse) updateCheckResponse {
	target := hostTarget(host, versions.OSBluefin)
	if target == "" {
		resp.Reason = "server has no bluefin release yet"
		return resp
	}
	resp.Target = target
	if rep.Version == "" {
		resp.Reason = "host reported no version"
		return resp
	}
	if rep.Version == target {
		resp.Reason = "bluefin up to date"
		return resp
	}
	resp.RebootRequired = true
	resp.Reason = bluefinUpdateReason + target + pinnedSuffix(host, target)
	return resp
}

// imageHostReason is the answer for a host that rebases onto an OSTree
// image: Booty no longer mirrors images, so it has no digest to compare
// against and leaves the reboot to the host's own rpm-ostree upgrade.
const imageHostReason = "image hosts update through rpm-ostree; booty does not track image digests"

func evaluateOSTree(host *hardware.Host, rep updateReport, resp updateCheckResponse) updateCheckResponse {
	if host.OSTreeImage != "" {
		resp.Target = host.OSTreeImage
		resp.Reason = imageHostReason
		return resp
	}
	return evaluateCoreOSVersion(host, rep, resp)
}

func evaluateCoreOSVersion(host *hardware.Host, rep updateReport, resp updateCheckResponse) updateCheckResponse {
	target := hostTarget(host, versions.OSCoreOS)
	if target == "" {
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
	resp.Reason = "coreos " + rep.Version + " differs from served " + target + pinnedSuffix(host, target)
	return resp
}

// normalizeImageRef strips the rpm-ostree transport prefixes and, for
// hosts rebased while Booty still mirrored images, its old client-facing
// registry, so the reference matches what hosts are registered with (e.g.
// ghcr.io/ublue-os/bazzite:stable, a Universal Blue image booted as
// os=coreos).
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
