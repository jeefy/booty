// Package fake is an httptest Kubernetes API server backed by JSON
// fixtures for the k8s, actuator and server tests. It records every
// request so a test can assert that a read-only path never wrote.
package fake

import (
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jeefy/booty/pkg/cluster/k8s"
	"github.com/jeefy/booty/pkg/kubeadm"
)

// API serves Fixtures (request path, plus "?<fieldSelector>" when one
// was sent) and records writes: Patches by path, Evictions and Deleted by
// namespace/name, Created manifests by namespace.
type API struct {
	mu        sync.Mutex
	Fixtures  map[string][]byte
	Requests  []string
	Patches   map[string][]string
	Evictions map[string]int
	// Refuse answers that many 429s (PodDisruptionBudget style, Retry-After
	// 1) to a namespace/name before evicting it.
	Refuse  map[string]int
	Created map[string][][]byte
	Deleted []string
	// Fail, when non-zero, is the status every request gets.
	Fail int
	// Token, when set, is what the bearer token must be.
	Token string
}

// New starts a TLS test server and returns it with a client that trusts it
// and authenticates with the expected token.
func New(t *testing.T) (*API, *k8s.Client, *httptest.Server) {
	t.Helper()
	api := &API{Fixtures: map[string][]byte{}, Patches: map[string][]string{}, Evictions: map[string]int{}, Refuse: map[string]int{}, Created: map[string][][]byte{}}
	srv := httptest.NewTLSServer(api)
	t.Cleanup(srv.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	client := k8s.FromConfig(kubeadm.KubeConfig{APIServer: srv.URL, Token: "t0k3n", CAData: ca})
	api.Token = "t0k3n"
	return api, client, srv
}

// Load reads the standard fixtures (nodes.json, pods-ehrlitan.json,
// daemonsets.json) from dir; every node also answers by name.
func (f *API) Load(t *testing.T, dir string) {
	t.Helper()
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Fixtures["/api/v1/nodes"] = read("nodes.json")
	f.Fixtures["/api/v1/pods?spec.nodeName=ehrlitan"] = read("pods-ehrlitan.json")
	f.Fixtures["/apis/apps/v1/daemonsets"] = read("daemonsets.json")
	var nodes struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(f.Fixtures["/api/v1/nodes"], &nodes); err != nil {
		t.Fatal(err)
	}
	for _, raw := range nodes.Items {
		var meta struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatal(err)
		}
		f.Fixtures["/api/v1/nodes/"+meta.Metadata.Name] = raw
	}
}

// Set installs a fixture body for a path.
func (f *API) Set(path string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Fixtures[path] = body
}

// Writes returns every recorded request that was not a GET.
func (f *API) Writes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.Requests {
		if !strings.HasPrefix(r, "GET ") {
			out = append(out, r)
		}
	}
	return out
}

func (f *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Requests = append(f.Requests, r.Method+" "+r.URL.Path)
	if f.Token != "" && r.Header.Get("Authorization") != "Bearer "+f.Token {
		status(w, http.StatusUnauthorized, "Unauthorized", "no token")
		return
	}
	if f.Fail != 0 {
		status(w, f.Fail, http.StatusText(f.Fail), "scripted failure")
		return
	}
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == http.MethodGet:
		key := r.URL.Path
		if sel := r.URL.Query().Get("fieldSelector"); sel != "" {
			key += "?" + sel
		}
		w.Header().Set("Content-Type", "application/json")
		if fx, ok := f.Fixtures[key]; ok {
			_, _ = w.Write(fx)
			return
		}
		if strings.HasPrefix(key, "/api/v1/pods?") {
			_, _ = w.Write([]byte(`{"kind":"PodList","items":[]}`))
			return
		}
		status(w, http.StatusNotFound, "NotFound", key+" not found")
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/v1/nodes/"):
		if r.Header.Get("Content-Type") != "application/strategic-merge-patch+json" {
			status(w, http.StatusUnsupportedMediaType, "UnsupportedMediaType", r.Header.Get("Content-Type"))
			return
		}
		f.Patches[r.URL.Path] = append(f.Patches[r.URL.Path], string(body))
		_, _ = w.Write([]byte(`{}`))
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/eviction"):
		parts := strings.Split(r.URL.Path, "/")
		key := parts[4] + "/" + parts[6]
		if f.Refuse[key] > 0 {
			f.Refuse[key]--
			w.Header().Set("Retry-After", "1")
			status(w, http.StatusTooManyRequests, "TooManyRequests", "Cannot evict pod as it would violate the pod's disruption budget.")
			return
		}
		f.Evictions[key]++
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pods"):
		ns := strings.Split(r.URL.Path, "/")[4]
		f.Created[ns] = append(f.Created[ns], body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	case r.Method == http.MethodDelete:
		parts := strings.Split(r.URL.Path, "/")
		f.Deleted = append(f.Deleted, parts[4]+"/"+parts[6])
		status(w, http.StatusNotFound, "NotFound", "already gone")
	default:
		status(w, http.StatusMethodNotAllowed, "MethodNotAllowed", r.Method)
	}
}

func status(w http.ResponseWriter, code int, reason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"kind": "Status", "code": code, "reason": reason, "message": message})
}
