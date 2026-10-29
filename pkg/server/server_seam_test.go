package server

import (
	"sync"
	"time"

	"github.com/jeefy/booty/pkg/autopilot"
	"github.com/jeefy/booty/pkg/cluster"
	"github.com/jeefy/booty/pkg/kubeadm"
	"github.com/jeefy/booty/pkg/power"
)

// The tests drive the most recently built Server (or a bare one before
// any NewHandler call), mirroring the old package-level dependencies.
var (
	curMu  sync.Mutex
	curSrv *Server
)

func init() {
	serverCreated = func(s *Server) {
		curMu.Lock()
		curSrv = s
		curMu.Unlock()
	}
}

func cur() *Server {
	curMu.Lock()
	defer curMu.Unlock()
	if curSrv == nil {
		curSrv = &Server{nowFunc: time.Now}
	}
	return curSrv
}

func setAutopilot(a *autopilot.Autopilot)  { cur().setAutopilot(a) }
func setJoinMinter(m *kubeadm.Minter)      { cur().setJoinMinter(m) }
func setClusterManager(m *cluster.Manager) { cur().setClusterManager(m) }
func setPowerTracker(t *power.Tracker)     { cur().setPowerTracker(t) }
