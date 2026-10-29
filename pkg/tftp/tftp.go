package tftp

import (
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/pin/tftp/v3"
	"github.com/spf13/viper"
)

const bootyIPXE = "booty.ipxe"

// Block sizes a TFTP server may negotiate (RFC 2348): the option allows
// 8..65464, but iPXE and PXE ROMs expect at least the classic 512.
const (
	MinBlockSize = 512
	MaxBlockSize = 65464
)

// ValidateBlockSize rejects block sizes outside MinBlockSize..MaxBlockSize.
// 0 means the library default (512) and is accepted.
func ValidateBlockSize(n int) error {
	if n == 0 {
		return nil
	}
	if n < MinBlockSize || n > MaxBlockSize {
		return fmt.Errorf("TFTP block size %d out of range: must be between %d and %d", n, MinBlockSize, MaxBlockSize)
	}
	return nil
}

type Config struct {
	Port int
	// BlockSize is the largest block size negotiated with clients; 0 keeps
	// the library default (512).
	BlockSize int
	// BootFiles serves the embedded iPXE binaries (config.BootFileNames) from
	// the root of the TFTP namespace, taking precedence over DataDir.
	BootFiles fs.FS
	// DataDir confines every other request; empty means --dataDir.
	DataDir string
	// ServerHostPort is the host[:port] the booty.ipxe stub chains to; nil
	// means config.ServerHostPort.
	ServerHostPort func() string
}

// withDefaults fills DataDir and ServerHostPort from the global
// configuration, once, so request handling reads nothing global.
func (c Config) withDefaults() Config {
	if c.DataDir == "" {
		c.DataDir = viper.GetString(config.DataDir)
	}
	if c.ServerHostPort == nil {
		c.ServerHostPort = config.ServerHostPort
	}
	return c
}

// handler answers read requests from its own Config; every Server has one.
type handler struct {
	cfg Config
}

type Server struct {
	srv  *tftp.Server
	conn *net.UDPConn
	h    *handler
}

// Start binds the UDP socket synchronously (so bind errors are returned) and
// serves in the background. Fatal serve errors are reported on errCh.
func Start(c Config, errCh chan<- error) (*Server, error) {
	if err := ValidateBlockSize(c.BlockSize); err != nil {
		return nil, err
	}
	h := &handler{cfg: c.withDefaults()}
	srv := tftp.NewServer(h.read, nil)
	if c.BlockSize != 0 {
		srv.SetBlockSize(c.BlockSize)
	}
	srv.EnableSinglePort()
	srv.SetTimeout(60 * time.Second)

	addr := &net.UDPAddr{Port: c.Port}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("tftp listen on %s: %w", addr, err)
	}
	go func() {
		if err := srv.Serve(conn); err != nil {
			errCh <- fmt.Errorf("tftp server: %w", err)
		}
	}()
	slog.Info("TFTP server started", "port", c.Port, "blockSize", c.BlockSize)
	return &Server{srv: srv, conn: conn, h: h}, nil
}

// Addr is the bound local address (useful with Port 0).
func (s *Server) Addr() *net.UDPAddr {
	a, _ := s.conn.LocalAddr().(*net.UDPAddr)
	return a
}

// Shutdown stops accepting requests and waits (bounded) for in-flight
// transfers. The socket is closed first because pin/tftp's single-port loop
// blocks in ReadFrom without a deadline and would otherwise never observe
// the quit signal.
func (s *Server) Shutdown(timeout time.Duration) {
	config.CloseQuietly(s.conn, "tftp socket")
	done := make(chan struct{})
	go func() {
		s.srv.Shutdown()
		close(done)
	}()
	select {
	case <-done:
		slog.Info("TFTP server stopped")
	case <-time.After(timeout):
		slog.Warn("TFTP shutdown timed out")
	}
}

func (h *handler) read(filename string, rf io.ReaderFrom) error {
	var remoteIP net.IP
	if ot, ok := rf.(tftp.OutgoingTransfer); ok {
		remoteIP = ot.RemoteAddr().IP
	}
	slog.Info("TFTP get", "filename", filename, "from", remoteIP)

	r, err := h.open(filename)
	if err != nil {
		slog.Warn("TFTP request rejected", "filename", filename, "from", remoteIP, "error", err)
		return err
	}
	defer config.CloseQuietly(r, filename)

	n, err := rf.ReadFrom(r)
	if err != nil {
		slog.Error("TFTP transfer failed", "filename", filename, "error", err)
		return err
	}
	slog.Info("TFTP sent", "bytes", n, "filename", filename)
	return nil
}

func stringReader(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}

// open resolves a TFTP filename to its content: the generated booty.ipxe
// stub, an embedded iPXE binary, or a file confined to DataDir.
func (h *handler) open(filename string) (io.ReadCloser, error) {
	if strings.ContainsRune(filename, 0) {
		return nil, fmt.Errorf("filename contains NUL byte")
	}
	if strings.HasPrefix(filename, "/") {
		return nil, fmt.Errorf("absolute paths are not served")
	}

	if filename == bootyIPXE {
		return stringReader(IPXEStub(h.cfg.ServerHostPort())), nil
	}
	if h.cfg.BootFiles != nil && config.IsBootFile(filename) {
		return h.cfg.BootFiles.Open(filename)
	}

	root, err := os.OpenRoot(h.cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("opening data dir: %w", err)
	}
	defer config.CloseQuietly(root, "data root")
	f, err := root.Open(filename)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		config.CloseQuietly(f, filename)
		return nil, err
	}
	if info.IsDir() {
		config.CloseQuietly(f, filename)
		return nil, fmt.Errorf("%s is a directory", filename)
	}
	return f, nil
}

// IPXEStub is the tiny script served over TFTP as booty.ipxe; iPXE expands
// ${mac} itself and chains to the HTTP endpoint which does the real work.
func IPXEStub(serverHostPort string) string {
	return fmt.Sprintf("#!ipxe\nchain http://%s/booty.ipxe?mac=${mac}\n", serverHostPort)
}

// SecureBootAutoexec is served as /boot/sb/autoexec.ipxe. The signed iPXE
// carries no Booty script, but an HTTP-booted iPXE fetches autoexec.ipxe
// from the directory it was loaded from before anything else, so this is
// how it reaches /booty.ipxe. It runs before iPXE has configured the NIC,
// hence the dhcp step; the URL is Booty's own, not ${next-server}.
func SecureBootAutoexec(serverHostPort string) string {
	return fmt.Sprintf(`#!ipxe
:start
dhcp || goto retry
chain http://%[1]s/booty.ipxe?mac=${mac}&sb=1 || goto shell
exit
:retry
sleep 5
goto start
:shell
echo Booty: could not reach %[1]s - dropping to the iPXE shell
shell
`, serverHostPort)
}
