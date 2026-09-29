package actuator

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

type sshServer struct {
	t        *testing.T
	ln       net.Listener
	config   *ssh.ServerConfig
	mu       sync.Mutex
	commands []string
	users    []string
}

func newSSHServer(t *testing.T, authorized ssh.PublicKey) *sshServer {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	s := &sshServer{t: t}
	s.config = &ssh.ServerConfig{
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) != string(authorized.Marshal()) {
				return nil, errors.New("unknown key")
			}
			s.mu.Lock()
			s.users = append(s.users, conn.User())
			s.mu.Unlock()
			return &ssh.Permissions{}, nil
		},
	}
	s.config.AddHostKey(hostSigner)
	s.ln, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.ln.Close() })
	go s.serve()
	return s
}

func (s *sshServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *sshServer) handle(nc net.Conn) {
	conn, chans, reqs, err := ssh.NewServerConn(nc, s.config)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		if ch.ChannelType() != "session" {
			_ = ch.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		channel, requests, err := ch.Accept()
		if err != nil {
			return
		}
		go func() {
			for req := range requests {
				if req.Type != "exec" {
					_ = req.Reply(false, nil)
					continue
				}
				var payload struct{ Command string }
				_ = ssh.Unmarshal(req.Payload, &payload)
				s.mu.Lock()
				s.commands = append(s.commands, payload.Command)
				s.mu.Unlock()
				_ = req.Reply(true, nil)
				_, _ = channel.Write([]byte("rebooting\n"))
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
				_ = channel.Close()
			}
		}()
	}
}

func (s *sshServer) addr() string { return s.ln.Addr().String() }

func writeClientKey(t *testing.T, dir string) (string, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return path, sshPub
}

func TestSSHRebootPinsHostKeyAndRunsTheRightCommand(t *testing.T) {
	dir := t.TempDir()
	keyPath, pub := writeClientKey(t, dir)
	srv := newSSHServer(t, pub)
	host, portStr, _ := net.SplitHostPort(srv.addr())
	port, _ := strconv.Atoi(portStr)
	known := filepath.Join(dir, "autopilot", "known_hosts")
	s := &SSH{KeyPath: keyPath, KnownHosts: known, Port: port}
	ctx := context.Background()

	if err := s.Reboot(ctx, Host{Hostname: "aren", OS: "flatcar", IP: host}); err != nil {
		t.Fatal(err)
	}
	if err := s.Reboot(ctx, Host{Hostname: "bf", OS: "bluefin", IP: host}); err != nil {
		t.Fatal(err)
	}
	if err := s.Reboot(ctx, Host{Hostname: "fc", OS: "coreos", IP: host}); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	users, commands := srv.users, srv.commands
	srv.mu.Unlock()
	if strings.Join(users, ",") != "core,root,core" {
		t.Fatalf("users: %v", users)
	}
	if strings.Join(commands, "|") != "sudo systemctl reboot|systemctl reboot|sudo systemctl reboot" {
		t.Fatalf("commands: %v", commands)
	}
	pinned, err := os.ReadFile(known)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(pinned)), "\n") + 1; lines != 1 || !strings.Contains(string(pinned), "ssh-ed25519") {
		t.Fatalf("known_hosts must hold the one pinned key:\n%s", pinned)
	}
	if info, _ := os.Stat(known); info.Mode().Perm() != 0o600 {
		t.Fatalf("known_hosts mode %v", info.Mode())
	}

	other := newSSHServer(t, pub)
	otherHost, otherPort, _ := net.SplitHostPort(other.addr())
	rewritten := strings.Replace(string(pinned), "["+host+"]:"+portStr, "["+otherHost+"]:"+otherPort, 1)
	if rewritten == string(pinned) {
		t.Fatalf("expected a [host]:port entry in %q", pinned)
	}
	if err := os.WriteFile(known, []byte(rewritten), 0o600); err != nil {
		t.Fatal(err)
	}
	p2, _ := strconv.Atoi(otherPort)
	s2 := &SSH{KeyPath: keyPath, KnownHosts: known, Port: p2}
	err = s2.Reboot(ctx, Host{Hostname: "aren", OS: "flatcar", IP: otherHost})
	if !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("a different host key for a pinned address must be refused: %v", err)
	}
	other.mu.Lock()
	defer other.mu.Unlock()
	if len(other.commands) != 0 {
		t.Fatalf("no command may run after a host key mismatch: %v", other.commands)
	}
}

func TestSSHWithoutKey(t *testing.T) {
	s := &SSH{KeyPath: filepath.Join(t.TempDir(), "missing"), KnownHosts: filepath.Join(t.TempDir(), "kh")}
	err := s.Reboot(context.Background(), Host{Hostname: "x", IP: "127.0.0.1", OS: "flatcar"})
	if err == nil || !strings.Contains(err.Error(), "rebootSSHKey") {
		t.Fatalf("missing key: %v", err)
	}
	if err := s.Reboot(context.Background(), Host{OS: "flatcar"}); err == nil {
		t.Fatal("no address must fail")
	}
	if err := s.Prepare(context.Background(), Host{Hostname: "x"}); err != nil {
		t.Fatalf("prepare without a cluster client is a no-op: %v", err)
	}
	if err := s.Finish(context.Background(), Host{Hostname: "x"}); err != nil {
		t.Fatalf("finish without a cluster client is a no-op: %v", err)
	}
}
