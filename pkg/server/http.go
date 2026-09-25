package server

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/jeefy/booty/pkg/versions"
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

// Options selects where the Web UI is served from: the embedded build when
// it contains index.html, otherwise WebDir on disk.
type Options struct {
	WebFS  fs.FS
	WebDir string
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
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleRoot)
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/ignition.json", handleIgnitionRequest)
	mux.HandleFunc("/booty.ipxe", handleIPXERequest)
	mux.HandleFunc("/version.txt", handleVersionRequest)
	mux.HandleFunc("/version.json", handleVersionRequest)
	mux.HandleFunc("/hosts", handleHostsRequest)
	mux.HandleFunc("/register", handleRegistrationRequest)
	mux.HandleFunc("/unregister", handleUnregistrationRequest)
	mux.HandleFunc("/booty.json", handleDataRequest)
	mux.HandleFunc("/info", handleInfoRequest)
	mux.HandleFunc("/flatcar/pin", handleFlatcarPinRequest)
	mux.HandleFunc("/registry", handleRegistryRequest)
	mux.Handle("/data/", http.StripPrefix("/data/", newDataHandler(viper.GetString(config.DataDir))))
	mux.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(uiFileSystem(o))))

	ociRegistry := registry.New(registry.WithBlobHandler(registry.NewDiskBlobHandler(versions.RegistryBlobDir())))
	mux.Handle("/v2/", ociRegistry)

	return logRequest(mux)
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
	quiet := []string{"/healthz", "/ui/", "/data/", "/v2/"}
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
// and Booty's own state files (hardware map, pin, temp files, registry
// blobs) are hidden.
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

func hostFromQuery(r *http.Request) (string, error) {
	raw := r.URL.Query().Get("mac")
	if raw == "" {
		return "", nil
	}
	return hardware.NormalizeMAC(raw)
}
