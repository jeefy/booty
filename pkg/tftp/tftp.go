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

type Config struct {
	Port      int
	BlockSize int
	// BootFiles serves the embedded iPXE binaries (config.BootFileNames) from
	// the root of the TFTP namespace, taking precedence over DataDir.
	BootFiles fs.FS
}

var cfg Config

type Server struct {
	srv  *tftp.Server
	conn *net.UDPConn
}

// Start binds the UDP socket synchronously (so bind errors are returned) and
// serves in the background. Fatal serve errors are reported on errCh.
func Start(c Config, errCh chan<- error) (*Server, error) {
	cfg = c
	srv := tftp.NewServer(readHandler, nil)
	srv.SetBlockSize(c.BlockSize)
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
	return &Server{srv: srv, conn: conn}, nil
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

func readHandler(filename string, rf io.ReaderFrom) error {
	var remoteIP net.IP
	if ot, ok := rf.(tftp.OutgoingTransfer); ok {
		remoteIP = ot.RemoteAddr().IP
	}
	slog.Info("TFTP get", "filename", filename, "from", remoteIP)

	r, err := openRequest(filename)
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

// openRequest resolves a TFTP filename to its content: the generated
// booty.ipxe stub, an embedded iPXE binary, or a file confined to DataDir.
func openRequest(filename string) (io.ReadCloser, error) {
	if strings.ContainsRune(filename, 0) {
		return nil, fmt.Errorf("filename contains NUL byte")
	}
	if strings.HasPrefix(filename, "/") {
		return nil, fmt.Errorf("absolute paths are not served")
	}

	if filename == bootyIPXE {
		return stringReader(IPXEStub(config.ServerHostPort())), nil
	}
	if cfg.BootFiles != nil && config.IsBootFile(filename) {
		return cfg.BootFiles.Open(filename)
	}

	root, err := os.OpenRoot(viper.GetString(config.DataDir))
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
