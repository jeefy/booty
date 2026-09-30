package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/jeefy/booty/pkg/cluster"
	"github.com/jeefy/booty/pkg/cluster/pki"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/state"
)

// clusterManager is set from Options.Cluster; nil means "not configured",
// in which case GET /cluster describes the flags without CA or tokens.
var clusterManager *cluster.Manager

func setClusterManager(m *cluster.Manager) {
	clusterManager = m
}

// clusterProbeTimeout bounds what one GET /cluster waits for a cold API
// probe. The probes are cached (Chooser.Inspect, Client.Reachable), so
// the typical request, and every request without a client, touches no
// network at all.
const clusterProbeTimeout = 3 * time.Second

const (
	clusterSourceManaged  = "managed"
	clusterSourceExternal = "external"
)

type clusterHost struct {
	MAC      string `json:"mac"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Role     string `json:"role"`
	Booted   string `json:"booted"`
	// SecureBoot mirrors hardware.Host.SecureBoot: the host's last boot
	// script was fetched through the Secure Boot path.
	SecureBoot bool `json:"secureBoot"`
}

// clusterResponse is what GET /cluster returns. It carries no key
// material: the CA is represented by its fingerprint only.
//
// Source says whose facts ready, endpoint and caFingerprint are. With a
// managed control plane ("managed") they are Booty's: ready means the
// control-plane host reported that kubeadm init finished and the API
// server answers /readyz (POST /cluster/ready), the endpoint is the flag
// or the control-plane host, the fingerprint is Booty's own CA. With an
// external one ("external") they come from the API server Booty talks to:
// ready is connected (there is no bootstrap to wait for), the endpoint is
// the flag or that server's host:port, the fingerprint is the CA Booty
// verifies it with. Connected, nodes and apiServer describe the live
// connection in both modes and are false/0/"" when Booty has no client.
type clusterResponse struct {
	Distribution  string        `json:"distribution"`
	ControlPlane  string        `json:"controlPlane"`
	Source        string        `json:"source"`
	Endpoint      string        `json:"endpoint"`
	APIServer     string        `json:"apiServer"`
	CNI           string        `json:"cni"`
	Ready         bool          `json:"ready"`
	ReadyAt       string        `json:"readyAt,omitempty"`
	Connected     bool          `json:"connected"`
	Nodes         int           `json:"nodes"`
	CAFingerprint string        `json:"caFingerprint"`
	Hosts         []clusterHost `json:"hosts"`
	Warnings      []string      `json:"warnings"`
}

func handleClusterRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), clusterProbeTimeout)
	defer cancel()
	writeJSON(w, http.StatusOK, clusterStatus(ctx, hardware.Snapshot().Hosts))
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

func clusterStatus(ctx context.Context, hosts map[string]*hardware.Host) clusterResponse {
	m := clusterManager
	if m == nil {
		m = &cluster.Manager{Settings: cluster.FromConfig()}
	}
	api, hasAPI := clusterAPI(ctx)
	resp := clusterResponse{
		Distribution: string(m.Settings.Distribution),
		ControlPlane: string(m.Settings.ControlPlane),
		Source:       clusterSourceExternal,
		APIServer:    api.server,
		CNI:          string(m.Settings.CNI),
		Connected:    api.connected,
		Nodes:        api.nodes,
		Hosts:        []clusterHost{},
		Warnings:     append(m.Warnings(hosts), secureBootWarnings(hosts)...),
	}
	if m.Settings.Managed() {
		resp.Source = clusterSourceManaged
		ready := state.GetClusterReady()
		resp.Ready, resp.ReadyAt = ready.Ready, ready.ReadyAt
	} else {
		resp.Ready = api.connected
	}
	if hasAPI && api.err != nil && (!m.Settings.Managed() || resp.Ready) {
		resp.Warnings = append(resp.Warnings, api.warning())
	}
	switch {
	case m.PKI != nil:
		resp.CAFingerprint = m.PKI.Fingerprint()
	case !m.Settings.Managed() && len(api.ca) > 0:
		fingerprint, err := pki.FingerprintPEM(api.ca)
		if err != nil {
			slog.Debug("API server CA has no usable certificate", "error", err)
		}
		resp.CAFingerprint = fingerprint
	}
	resp.Endpoint = clusterEndpoint(m, hosts, api)
	for _, h := range hosts {
		resp.Hosts = append(resp.Hosts, clusterHost{MAC: h.MAC, Hostname: h.Hostname, OS: h.OS, Role: string(cluster.RoleOf(h)), Booted: h.Booted, SecureBoot: h.SecureBoot})
	}
	sort.Slice(resp.Hosts, func(i, j int) bool { return resp.Hosts[i].MAC < resp.Hosts[j].MAC })
	return resp
}

// clusterEndpoint is the host[:port] GET /cluster reports:
// --controlPlaneEndpoint when set; for an external control plane the
// host:port of the API server Booty talks to; otherwise the single
// registered control-plane host's IP, which only a managed control plane
// is expected to have.
func clusterEndpoint(m *cluster.Manager, hosts map[string]*hardware.Host, api clusterAPIView) string {
	if m.Settings.Endpoint != "" {
		return m.Settings.Endpoint
	}
	if !m.Settings.Managed() && api.server != "" {
		if u, err := url.Parse(api.server); err == nil && u.Host != "" {
			return u.Host
		}
	}
	endpoint, err := m.Endpoint(hosts)
	switch {
	case err == nil:
		return endpoint
	case m.Settings.Managed():
		slog.Debug("Cluster endpoint unresolved", "error", err)
	}
	return ""
}

// clusterAPIView is what GET /cluster knows about the API server Booty
// talks to. err is the probe's failure, or what a reachable server
// answered with (RBAC, credentials) when it did not answer 2xx.
type clusterAPIView struct {
	server    string
	ca        []byte
	connected bool
	nodes     int
	err       error
}

func (v clusterAPIView) warning() string {
	if !v.connected {
		return fmt.Sprintf("API server %s unreachable: %v", v.server, v.err)
	}
	return fmt.Sprintf("API server %s: %v", v.server, v.err)
}

// clusterAPI probes through whichever client Booty already has, so no
// configuration is loaded twice and no second request is added: the
// autopilot's cluster client, whose cached Inspect also counts the nodes,
// else the token minter's, whose cached Reachable is one GET /version
// (the in-cluster fallback minter counts, so a Booty running in the
// cluster it serves always has one). ok is false when there is no client
// at all, at no cost.
func clusterAPI(ctx context.Context) (view clusterAPIView, ok bool) {
	if a := pilot; a != nil && a.Settings.Enabled() && a.Chooser != nil && a.Client.Configured() {
		st, _ := a.Chooser.Inspect(ctx)
		view = clusterAPIView{server: st.APIServer, ca: a.Client.API().CACert(), connected: st.Reachable, nodes: st.Nodes}
		if st.Error != "" {
			view.err = errors.New(st.Error)
		}
		return view, true
	}
	if m := joinMinter; m != nil && m.APIServer() != "" {
		view = clusterAPIView{server: m.APIServer(), ca: m.CACert()}
		view.connected, view.err = m.Reachable(ctx)
		return view, true
	}
	return clusterAPIView{}, false
}
