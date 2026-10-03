package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/autopilot"
	"github.com/jeefy/booty/pkg/cluster"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/kubeadm"
	"github.com/jeefy/booty/pkg/power"
	"github.com/jeefy/booty/pkg/tftp"
	"github.com/spf13/viper"
)

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		slog.Error("Encoding JSON response failed", "error", err)
		status = http.StatusInternalServerError
		data = []byte(`{"error":"internal error encoding response"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(data); err != nil {
		slog.Debug("Writing response failed", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

func writeText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write([]byte(body)); err != nil {
		slog.Debug("Writing response failed", "error", err)
	}
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return ""
	}
	return host
}

// Options selects where the Web UI is served from (the embedded build when
// it contains index.html, otherwise WebDir on disk) and which embedded iPXE
// binaries /boot/ exposes.
type Options struct {
	WebFS     fs.FS
	WebDir    string
	BootFiles fs.FS
	// Minter provides kubeadm join tokens for --kubeadmJoin=auto; nil means
	// build one from the in-cluster environment on first use.
	Minter *kubeadm.Minter
	// Cluster holds the cluster settings, CA and tokens for GET /cluster;
	// nil means the flags are described without any CA.
	Cluster *cluster.Manager
	// Autopilot answers GET /autopilot; nil reads as mode off.
	Autopilot *autopilot.Autopilot
	// Power answers GET /power and the power actions; nil serves the
	// recorded states read-only and refuses actions.
	Power *power.Tracker
}

func uiFileSystem(o Options) http.FileSystem {
	if o.WebFS != nil {
		if f, err := o.WebFS.Open("index.html"); err == nil {
			config.CloseQuietly(f, "embedded index.html")
			slog.Info("Serving embedded Web UI")
			return http.FS(o.WebFS)
		}
	}
	slog.Info("Serving Web UI from disk", "dir", o.WebDir)
	return http.Dir(o.WebDir)
}

func NewHandler(o Options) http.Handler {
	setJoinMinter(o.Minter)
	setClusterManager(o.Cluster)
	setAutopilot(o.Autopilot)
	setPowerTracker(o.Power)
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleRoot)
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/ignition.json", handleIgnitionRequest)
	mux.HandleFunc("/ignition/user.json", handleIgnitionUserRequest)
	mux.HandleFunc("/ignition/builtin.json", handleIgnitionBuiltinRequest)
	mux.HandleFunc("/update-check", handleUpdateCheckRequest)
	mux.HandleFunc("/booty.ipxe", handleIPXERequest)
	mux.HandleFunc("/version.txt", handleVersionRequest)
	mux.HandleFunc("/version.json", handleVersionRequest)
	mux.HandleFunc("/hosts", handleHostsRequest)
	mux.HandleFunc("/booted", handleBootedRequest)
	mux.HandleFunc("/health", handleHealthRequest)
	mux.HandleFunc("/register", handleRegistrationRequest)
	mux.HandleFunc("/unregister", handleUnregistrationRequest)
	mux.HandleFunc("/booty.json", handleDataRequest)
	mux.HandleFunc("/info", handleInfoRequest)
	mux.HandleFunc("/flatcar/pin", handleFlatcarPinRequest)
	mux.HandleFunc("/config", handleConfigRequest)
	mux.HandleFunc("/config/template", handleConfigTemplateRequest)
	mux.HandleFunc("/config/template/validate", handleConfigTemplateValidateRequest)
	mux.HandleFunc("/cluster", handleClusterRequest)
	mux.HandleFunc("/cluster/ready", handleClusterReadyRequest)
	mux.HandleFunc("/autopilot", handleAutopilotRequest)
	mux.HandleFunc("/autopilot/", handleAutopilotRequest)
	mux.HandleFunc("/power", handlePowerRequest)
	mux.HandleFunc("/power/", handlePowerRequest)
	mux.HandleFunc("/storage", handleStorageRequest)
	mux.Handle("/data/", http.StripPrefix("/data/", newDataHandler(viper.GetString(config.DataDir))))
	mux.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(uiFileSystem(o))))

	// /boot/ and /bluefin/ are routed before the mux: UEFI HTTP Boot
	// firmware and shim never follow redirects, and http.ServeMux answers
	// unclean paths such as /boot/sb//ipxe.efi with a 301. Both handlers
	// clean the path themselves.
	boot := bootHandler{files: o.BootFiles, secureBootDir: config.SecureBootPath()}
	return logRequest(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, bootPathPrefix) {
			boot.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, bluefinPathPrefix) {
			handleBluefinRequest(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}))
}

// Start binds the listener synchronously and serves in the background so
// callers know the port is open when Start returns. Serve failures other
// than a clean shutdown are reported on errCh.
func Start(o Options, errCh chan<- error) (*http.Server, error) {
	addr := fmt.Sprintf(":%d", viper.GetInt(config.HttpPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("http listen on %s: %w", addr, err)
	}

	s := &http.Server{
		Addr:              addr,
		Handler:           NewHandler(o),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       900 * time.Second,
		WriteTimeout:      900 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	go func() {
		if err := s.Serve(ln); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()
	slog.Info("HTTP server started", "addr", addr)
	return s, nil
}

func logRequest(handler http.Handler) http.Handler {
	quiet := []string{"/healthz", "/ui/", "/data/", "/boot/", "/update-check"}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		level := slog.LevelInfo
		for _, prefix := range quiet {
			if strings.HasPrefix(r.URL.Path, prefix) {
				level = slog.LevelDebug
				break
			}
		}
		slog.Log(r.Context(), level, "HTTP request", "remote", r.RemoteAddr, "method", r.Method, "path", r.URL.Path)
		handler.ServeHTTP(w, r)
	})
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	http.Redirect(w, r, "/ui/", http.StatusFound)
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// dataHandler serves DataDir read-only over /data/: no directory listings,
// and Booty's own state files (hardware map, pin, temp files, a leftover
// registry/ blob cache, the cluster CA and tokens) are hidden.
type dataHandler struct {
	root  *os.Root
	files http.Handler
}

func newDataHandler(dir string) http.Handler {
	root, err := os.OpenRoot(dir)
	if err != nil {
		slog.Error("Cannot open data directory; /data/ disabled", "dir", dir, "error", err)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, "not found")
		})
	}
	return &dataHandler{root: root, files: http.FileServerFS(root.FS())}
}

func deniedDataPath(p string) bool {
	clean := path.Clean("/" + p)
	rel := strings.TrimPrefix(clean, "/")
	hwMap := filepath.ToSlash(filepath.Clean(viper.GetString(config.HardwareMap)))
	switch {
	case rel == "" || rel == ".":
		return true
	case rel == hwMap || rel == config.FlatcarPinFile:
		return true
	case strings.HasSuffix(rel, ".tmp"):
		return true
	case rel == "registry" || strings.HasPrefix(rel, "registry/"):
		return true
	case rel == config.ClusterDir || strings.HasPrefix(rel, config.ClusterDir+"/"):
		return true
	}
	return false
}

func (h *dataHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if deniedDataPath(r.URL.Path) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	rel := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	info, err := h.root.Stat(rel)
	if err != nil || info.IsDir() {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	h.files.ServeHTTP(w, r)
}

// bootHandler serves the boot loaders firmware and iPXE fetch over HTTP:
// the embedded iPXE binaries at /boot/<name>, the Secure Boot bundle
// (data/secureboot/current/) at /boot/sb/<name> and /boot/secureboot/<path>,
// and the Flatcar CA at /boot/secureboot/flatcar-ca.{der,pem}. It is raw on
// purpose: repeated slashes are collapsed without a redirect, HEAD is
// answered with the same headers as GET, Content-Length is always set,
// .efi files are application/efi (UEFI firmware needs that or the suffix)
// and nothing is listed.
type bootHandler struct {
	files         fs.FS
	secureBootDir string
}

const (
	bootPathPrefix       = "/boot/"
	bootSBPrefix         = "sb/"
	bootSecureBootPrefix = "secureboot/"
	efiContentType       = "application/efi"
)

type bootFile struct {
	io.ReadCloser
	size int64
}

func (h bootHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	clean := path.Clean(r.URL.Path)
	if !strings.HasPrefix(clean, bootPathPrefix) || strings.HasSuffix(r.URL.Path, "/") || slices.Contains(strings.Split(r.URL.Path, "/"), "..") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	rel := strings.TrimPrefix(clean, bootPathPrefix)
	f, ok := h.open(rel)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	defer config.CloseQuietly(f, rel)

	contentType := "application/octet-stream"
	if strings.EqualFold(path.Ext(rel), ".efi") {
		contentType = efiContentType
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(f.size, 10))
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, f); err != nil {
		slog.Debug("Writing boot file failed", "name", rel, "error", err)
	}
}

// open resolves a cleaned path below /boot/ to a file. Embedded names are
// exact matches; everything under sb/ and secureboot/ is looked up inside
// data/secureboot/ through os.Root so no request can leave that tree.
func (h bootHandler) open(rel string) (*bootFile, bool) {
	switch {
	case rel == bootSBPrefix+"autoexec.ipxe":
		script := tftp.SecureBootAutoexec(config.ServerHostPort())
		return &bootFile{ReadCloser: io.NopCloser(strings.NewReader(script)), size: int64(len(script))}, true
	case strings.HasPrefix(rel, bootSBPrefix):
		return h.openSecureBoot(path.Join(config.SecureBootCurrentLink, strings.TrimPrefix(rel, bootSBPrefix)))
	case strings.HasPrefix(rel, bootSecureBootPrefix):
		sub := strings.TrimPrefix(rel, bootSecureBootPrefix)
		if f, ok := h.openSecureBoot(path.Join(config.SecureBootCurrentLink, sub)); ok {
			return f, true
		}
		if sub == config.SecureBootFlatcarCADER || sub == config.SecureBootFlatcarCAPEM {
			return h.openSecureBoot(sub)
		}
		return nil, false
	case h.files != nil && config.IsBootFile(rel):
		data, err := fs.ReadFile(h.files, rel)
		if err != nil {
			slog.Error("Embedded boot file unreadable", "name", rel, "error", err)
			return nil, false
		}
		return &bootFile{ReadCloser: io.NopCloser(bytes.NewReader(data)), size: int64(len(data))}, true
	}
	return nil, false
}

func (h bootHandler) openSecureBoot(rel string) (*bootFile, bool) {
	if h.secureBootDir == "" {
		return nil, false
	}
	if _, err := config.CleanRelPath(rel); err != nil {
		return nil, false
	}
	root, err := os.OpenRoot(h.secureBootDir)
	if err != nil {
		return nil, false
	}
	defer config.CloseQuietly(root, h.secureBootDir)
	f, err := root.Open(rel)
	if err != nil {
		return nil, false
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		config.CloseQuietly(f, rel)
		return nil, false
	}
	return &bootFile{ReadCloser: f, size: info.Size()}, true
}

func hostFromQuery(r *http.Request) (string, error) {
	raw := r.URL.Query().Get("mac")
	if raw == "" {
		return "", nil
	}
	return hardware.NormalizeMAC(raw)
}
