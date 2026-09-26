package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jeefy/booty/pkg/config"
)

// ClusterReady is the persisted record of the control-plane host reporting
// that kubeadm init finished and the API server answers.
type ClusterReady struct {
	Ready   bool   `json:"ready"`
	ReadyAt string `json:"readyAt,omitempty"`
	MAC     string `json:"mac,omitempty"`
}

var (
	clusterMu    sync.RWMutex
	clusterReady ClusterReady
)

func clusterReadyPath() string {
	return config.ClusterPath(config.ClusterReadyFile)
}

// LoadClusterReady reads --dataDir/cluster/ready.json; a missing file means
// not ready.
func LoadClusterReady() {
	data, err := os.ReadFile(clusterReadyPath())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Error("Error reading cluster ready state", "path", clusterReadyPath(), "error", err)
		}
		return
	}
	var r ClusterReady
	if err := json.Unmarshal(data, &r); err != nil {
		slog.Warn("Cluster ready state is not parseable; treating the cluster as not ready", "path", clusterReadyPath(), "error", err)
		return
	}
	clusterMu.Lock()
	clusterReady = r
	clusterMu.Unlock()
	if r.Ready {
		slog.Info("Cluster marked ready by a previous run", "readyAt", r.ReadyAt, "mac", r.MAC)
	}
}

// GetClusterReady returns the current record.
func GetClusterReady() ClusterReady {
	clusterMu.RLock()
	defer clusterMu.RUnlock()
	return clusterReady
}

// MarkClusterReady records that the control-plane host mac reported ready
// at now and persists it. A cluster that is already ready keeps its first
// readyAt, so the call is idempotent. It returns whether this call changed
// the state.
func MarkClusterReady(mac string, now time.Time) (bool, error) {
	clusterMu.Lock()
	defer clusterMu.Unlock()
	if clusterReady.Ready {
		return false, nil
	}
	r := ClusterReady{Ready: true, ReadyAt: now.UTC().Format(time.RFC3339), MAC: mac}
	data, err := json.Marshal(r)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(clusterReadyPath()), 0o700); err != nil {
		return false, fmt.Errorf("creating cluster dir: %w", err)
	}
	if err := config.WriteFileAtomic(clusterReadyPath(), append(data, '\n'), 0o600); err != nil {
		return false, err
	}
	clusterReady = r
	return true, nil
}

// ResetClusterReady forgets the ready state in memory; tests use it.
func ResetClusterReady() {
	clusterMu.Lock()
	clusterReady = ClusterReady{}
	clusterMu.Unlock()
}
