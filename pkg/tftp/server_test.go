package tftp

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/pin/tftp/v3"
	"github.com/spf13/viper"
)

func TestValidateBlockSize(t *testing.T) {
	for _, n := range []int{0, MinBlockSize, 1468, MaxBlockSize} {
		if err := ValidateBlockSize(n); err != nil {
			t.Errorf("%d: %v", n, err)
		}
	}
	for _, n := range []int{-1, 1, 8, MinBlockSize - 1, MaxBlockSize + 1, 1 << 20} {
		err := ValidateBlockSize(n)
		if err == nil {
			t.Errorf("%d should be rejected", n)
			continue
		}
		if !strings.Contains(err.Error(), "between 512 and 65464") {
			t.Errorf("%d: unclear error %q", n, err)
		}
	}
}

func TestStartRejectsBadBlockSize(t *testing.T) {
	if s, err := Start(Config{Port: 0, BlockSize: 100}, make(chan error, 1)); err == nil {
		s.Shutdown(time.Second)
		t.Fatal("Start accepted block size 100")
	}
}

// BlockSize 0 is the pin/tftp default (512), as before validation existed.
func TestStartWithDefaultBlockSize(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), bytes.Repeat([]byte("x"), 3*MinBlockSize+7), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Start(Config{Port: 0, BlockSize: 0, DataDir: dir}, make(chan error, 1))
	if err != nil {
		t.Fatalf("Start rejected block size 0: %v", err)
	}
	t.Cleanup(func() { s.Shutdown(2 * time.Second) })
	if got := tftpGet(t, s, "big.bin"); len(got) != 3*MinBlockSize+7 {
		t.Fatalf("got %d bytes", len(got))
	}
}

func tftpGet(t *testing.T, s *Server, name string) string {
	t.Helper()
	c, err := tftp.NewClient(fmt.Sprintf("127.0.0.1:%d", s.Addr().Port))
	if err != nil {
		t.Fatal(err)
	}
	c.SetTimeout(2 * time.Second)
	c.SetRetries(2)
	wt, err := c.Receive(name, "octet")
	if err != nil {
		t.Fatalf("receive %s from :%d: %v", name, s.Addr().Port, err)
	}
	var buf bytes.Buffer
	if _, err := wt.WriteTo(&buf); err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return buf.String()
}

// Two servers in one process each answer from their own Config: no
// package state is shared between them, and neither falls back to the
// global --dataDir/--serverIP once started.
func TestServersWithDifferentConfigsDoNotInterfere(t *testing.T) {
	mkServer := func(label string) *Server {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "who.txt"), []byte(label), 0o644); err != nil {
			t.Fatal(err)
		}
		host := label + ".example:8080"
		s, err := Start(Config{
			Port:           0,
			BlockSize:      MinBlockSize,
			BootFiles:      fstest.MapFS{"ipxe.efi": {Data: []byte("EFI-" + label)}},
			DataDir:        dir,
			ServerHostPort: func() string { return host },
		}, make(chan error, 1))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Shutdown(2 * time.Second) })
		return s
	}
	a := mkServer("a")
	b := mkServer("b")

	// Point the globals somewhere neither server uses.
	viper.Set(config.DataDir, t.TempDir())
	viper.Set(config.ServerIP, "203.0.113.1")
	t.Cleanup(func() { viper.Set(config.ServerIP, "") })

	for label, s := range map[string]*Server{"a": a, "b": b} {
		if got := tftpGet(t, s, "who.txt"); got != label {
			t.Errorf("server %s who.txt = %q", label, got)
		}
		if got, want := tftpGet(t, s, "ipxe.efi"), "EFI-"+label; got != want {
			t.Errorf("server %s ipxe.efi = %q, want %q", label, got, want)
		}
		if got, want := tftpGet(t, s, bootyIPXE), IPXEStub(label+".example:8080"); got != want {
			t.Errorf("server %s booty.ipxe = %q, want %q", label, got, want)
		}
	}
}
