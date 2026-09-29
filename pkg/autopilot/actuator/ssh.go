package actuator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jeefy/booty/pkg/cluster/k8s"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SSH reboots over SSH with the --rebootSSHKey private key: as root on
// Bluefin, as core with sudo on Flatcar and CoreOS. Host keys are pinned
// on first use into KnownHosts (trust-on-first-use) and must match after
// that. Prepare drains through Drain when a cluster client exists.
type SSH struct {
	KeyPath    string
	KnownHosts string
	// Drain is the cluster client used to cordon and evict before the
	// reboot; nil means no drain.
	Drain        *k8s.Client
	DrainTimeout time.Duration
	Port         int
	Timeout      time.Duration
	// Dial overrides the network dialer (tests point it at an in-process
	// server).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// ErrHostKeyChanged is returned when a pinned host key no longer matches.
var ErrHostKeyChanged = errors.New("ssh host key changed since it was pinned")

func (s *SSH) Name() string { return NameSSH }

// User is the login for os: root on Bluefin, core elsewhere.
func (s *SSH) User(os string) string {
	if os == "bluefin" {
		return "root"
	}
	return "core"
}

// Command is what runs on the host: systemctl reboot, through sudo for
// the core user.
func (s *SSH) Command(os string) string {
	if s.User(os) == "root" {
		return "systemctl reboot"
	}
	return "sudo systemctl reboot"
}

// Prepare cordons and drains through the API when a client is available.
func (s *SSH) Prepare(ctx context.Context, host Host) error {
	if s.Drain == nil || !s.Drain.Configured() {
		slog.Info("SSH actuator: no cluster client, rebooting without a drain", "host", host.Hostname)
		return nil
	}
	return (&API{Client: s.Drain, DrainTimeout: s.DrainTimeout}).Prepare(ctx, host)
}

// Reboot runs the reboot command on the host over SSH.
func (s *SSH) Reboot(ctx context.Context, host Host) error {
	addr := host.IP
	if addr == "" {
		addr = host.Hostname
	}
	if addr == "" {
		return errors.New("ssh: host has neither an IP nor a hostname")
	}
	port := s.Port
	if port == 0 {
		port = 22
	}
	out, err := s.Run(ctx, net.JoinHostPort(addr, fmt.Sprint(port)), s.User(host.OS), s.Command(host.OS))
	if err != nil {
		return fmt.Errorf("ssh %s@%s: %w", s.User(host.OS), addr, err)
	}
	slog.Info("Reboot requested over SSH", "host", host.Hostname, "user", s.User(host.OS), "command", s.Command(host.OS), "output", strings.TrimSpace(string(out)))
	return nil
}

// Finish uncordons through the API when a client is available.
func (s *SSH) Finish(ctx context.Context, host Host) error {
	if s.Drain == nil || !s.Drain.Configured() {
		return nil
	}
	return (&API{Client: s.Drain}).Finish(ctx, host)
}

// Run connects to addr as user, runs command and returns its combined
// output. The host key is checked against, or pinned into, KnownHosts.
func (s *SSH) Run(ctx context.Context, addr, user, command string) ([]byte, error) {
	key, err := os.ReadFile(s.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("reading --rebootSSHKey: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("parsing --rebootSSHKey: %w", err)
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: s.hostKeyCallback(),
		Timeout:         timeout,
	}
	dial := s.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: timeout}).DialContext
	}
	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	client := ssh.NewClient(c, chans, reqs)
	defer func() { _ = client.Close() }()
	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer func() { _ = session.Close() }()
	return session.CombinedOutput(command)
}

func (s *SSH) hostKeyCallback() ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if s.KnownHosts == "" {
			return errors.New("ssh: no known_hosts path configured")
		}
		if err := os.MkdirAll(filepath.Dir(s.KnownHosts), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(s.KnownHosts, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		check, err := knownhosts.New(s.KnownHosts)
		if err != nil {
			return err
		}
		err = check(hostname, remote, key)
		var keyErr *knownhosts.KeyError
		switch {
		case err == nil:
			return nil
		case errors.As(err, &keyErr) && len(keyErr.Want) > 0:
			return fmt.Errorf("%w: %s presents %s %s, known_hosts has %s", ErrHostKeyChanged, hostname, key.Type(), ssh.FingerprintSHA256(key), keyErr.Want[0].Key.Type())
		case errors.As(err, &keyErr):
			line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
			if _, err := f.Seek(0, 2); err != nil {
				return err
			}
			if _, err := f.WriteString(line + "\n"); err != nil {
				return err
			}
			slog.Info("SSH host key pinned on first use", "host", hostname, "type", key.Type(), "fingerprint", ssh.FingerprintSHA256(key), "file", s.KnownHosts)
			return nil
		}
		return err
	}
}
