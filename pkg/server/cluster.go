package server

import (
	"log/slog"
	"net/http"
	"sort"

	"github.com/jeefy/booty/pkg/cluster"
	"github.com/jeefy/booty/pkg/hardware"
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
// material: the CA is represented by its fingerprint only.
type clusterResponse struct {
	Distribution  string        `json:"distribution"`
	ControlPlane  string        `json:"controlPlane"`
	Endpoint      string        `json:"endpoint"`
	CNI           string        `json:"cni"`
	Ready         bool          `json:"ready"`
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

func clusterStatus(hosts map[string]*hardware.Host) clusterResponse {
	m := clusterManager
	if m == nil {
		m = &cluster.Manager{Settings: cluster.FromConfig()}
	}
	resp := clusterResponse{
		Distribution: string(m.Settings.Distribution),
		ControlPlane: string(m.Settings.ControlPlane),
		CNI:          string(m.Settings.CNI),
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
