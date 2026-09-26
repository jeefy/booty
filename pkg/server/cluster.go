package server

import (
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/jeefy/booty/pkg/cluster"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/state"
)

// clusterManager is set from Options.Cluster; nil means "not configured",
// in which case GET /cluster describes the flags without CA or tokens.
var clusterManager *cluster.Manager

func setClusterManager(m *cluster.Manager) {
	clusterManager = m
}

type clusterHost struct {
	MAC      string `json:"mac"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Role     string `json:"role"`
	Booted   string `json:"booted"`
}

// clusterResponse is what GET /cluster returns. It carries no key
// material: the CA is represented by its fingerprint only. Ready means the
// control-plane host reported that kubeadm init finished and the API
// server answers /readyz (POST /cluster/ready); it says nothing about the
// CNI or worker joins.
type clusterResponse struct {
	Distribution  string        `json:"distribution"`
	ControlPlane  string        `json:"controlPlane"`
	Endpoint      string        `json:"endpoint"`
	CNI           string        `json:"cni"`
	Ready         bool          `json:"ready"`
	ReadyAt       string        `json:"readyAt,omitempty"`
	CAFingerprint string        `json:"caFingerprint"`
	Hosts         []clusterHost `json:"hosts"`
	Warnings      []string      `json:"warnings"`
}

func handleClusterRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, clusterStatus(hardware.Snapshot().Hosts))
}

// handleClusterReadyRequest is POSTed by the control-plane host's
// booty-cluster-ready.service. Only a registered role: control-plane host
// may flip the flag; repeated calls are no-ops that keep the first readyAt.
func handleClusterReadyRequest(w http.ResponseWriter, r *http.Request) {
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
	host, found := hardware.Get(mac)
	if !found || !host.IsControlPlane() {
		writeError(w, http.StatusNotFound, "control-plane host not registered")
		return
	}
	changed, err := state.MarkClusterReady(mac, time.Now())
	if err != nil {
		slog.Error("Recording cluster ready failed", "mac", mac, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to record cluster ready")
		return
	}
	if changed {
		slog.Info("Control plane reported ready", "mac", mac, "hostname", host.Hostname, "ip", remoteIP(r))
	}
	writeJSON(w, http.StatusOK, state.GetClusterReady())
}

func clusterStatus(hosts map[string]*hardware.Host) clusterResponse {
	m := clusterManager
	if m == nil {
		m = &cluster.Manager{Settings: cluster.FromConfig()}
	}
	ready := state.GetClusterReady()
	resp := clusterResponse{
		Distribution: string(m.Settings.Distribution),
		ControlPlane: string(m.Settings.ControlPlane),
		CNI:          string(m.Settings.CNI),
		Ready:        ready.Ready,
		ReadyAt:      ready.ReadyAt,
		Hosts:        []clusterHost{},
		Warnings:     m.Warnings(hosts),
	}
	if m.PKI != nil {
		resp.CAFingerprint = m.PKI.Fingerprint()
	}
	endpoint, err := m.Endpoint(hosts)
	switch {
	case err == nil:
		resp.Endpoint = endpoint
	case m.Settings.Managed():
		slog.Debug("Cluster endpoint unresolved", "error", err)
	}
	for _, h := range hosts {
		resp.Hosts = append(resp.Hosts, clusterHost{MAC: h.MAC, Hostname: h.Hostname, OS: h.OS, Role: string(cluster.RoleOf(h)), Booted: h.Booted})
	}
	sort.Slice(resp.Hosts, func(i, j int) bool { return resp.Hosts[i].MAC < resp.Hosts[j].MAC })
	return resp
}
