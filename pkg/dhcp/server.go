package dhcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/jeefy/booty/pkg/config"
)

const maxPacket = 1500

// Server is the ProxyDHCP listener pair (port 67 for DHCPDISCOVER, port
// 4011 for the PXE DHCPREQUEST).
type Server struct {
	cfg   Config
	conns []*net.UDPConn
	wg    sync.WaitGroup
}

// Start binds both UDP sockets synchronously (so bind errors are returned)
// and serves in the background. Fatal read errors are reported on errCh.
func Start(c Config, errCh chan<- error) (*Server, error) {
	c = c.withDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	s := &Server{cfg: c}
	for _, port := range []int{c.Port67, c.Port4011} {
		conn, err := listen(c.Listen, port)
		if err != nil {
			s.closeAll()
			return nil, err
		}
		s.conns = append(s.conns, conn)
		s.wg.Add(1)
		go s.serve(conn, errCh)
	}
	slog.Info("ProxyDHCP server started", "listen", describeListen(c.Listen), "ports", []int{c.Port67, c.Port4011}, "serverIP", c.ServerIP, "relay", c.Relay)
	return s, nil
}

// Addrs returns the bound local addresses, in the order port67, port4011.
func (s *Server) Addrs() []*net.UDPAddr {
	out := make([]*net.UDPAddr, 0, len(s.conns))
	for _, c := range s.conns {
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
			out = append(out, a)
		}
	}
	return out
}

// Shutdown closes the sockets and waits (bounded) for the read loops.
func (s *Server) Shutdown(timeout time.Duration) {
	s.closeAll()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		slog.Info("ProxyDHCP server stopped")
	case <-time.After(timeout):
		slog.Warn("ProxyDHCP shutdown timed out")
	}
}

func (s *Server) closeAll() {
	for _, c := range s.conns {
		config.CloseQuietly(c, "proxydhcp socket "+c.LocalAddr().String())
	}
}

func (s *Server) serve(conn *net.UDPConn, errCh chan<- error) {
	defer s.wg.Done()
	buf := make([]byte, maxPacket)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			errCh <- fmt.Errorf("proxydhcp read on %s: %w", conn.LocalAddr(), err)
			return
		}
		pkt, err := dhcpv4.FromBytes(buf[:n])
		if err != nil {
			slog.Debug("ProxyDHCP dropping malformed packet", "from", src, "error", err)
			continue
		}
		reply, ok := handle(pkt, s.cfg)
		if !ok {
			continue
		}
		dst := replyDestination(pkt, src)
		if _, err := conn.WriteToUDP(reply.ToBytes(), dst); err != nil {
			slog.Warn("ProxyDHCP reply failed", "to", dst, "error", err)
		}
	}
}

// listen binds 0.0.0.0:port (optionally restricted to an interface) or
// ip:port, with SO_BROADCAST so replies can go to 255.255.255.255.
func listen(target string, port int) (*net.UDPConn, error) {
	addr := &net.UDPAddr{Port: port}
	var iface string
	switch {
	case target == "":
	case net.ParseIP(target) != nil:
		ip := net.ParseIP(target)
		if ip.To4() == nil {
			return nil, fmt.Errorf("proxydhcp listen %q: not an IPv4 address", target)
		}
		addr.IP = ip
	default:
		if _, err := net.InterfaceByName(target); err != nil {
			return nil, fmt.Errorf("proxydhcp listen %q: neither an IPv4 address nor an interface: %w", target, err)
		}
		iface = target
	}

	lc := net.ListenConfig{Control: controlFunc(iface)}
	pc, err := lc.ListenPacket(context.Background(), "udp4", addr.String())
	if err != nil {
		return nil, fmt.Errorf("proxydhcp listen on %s: %w", addr, err)
	}
	conn, ok := pc.(*net.UDPConn)
	if !ok {
		config.CloseQuietly(pc, "proxydhcp socket")
		return nil, fmt.Errorf("proxydhcp listen on %s: unexpected socket type %T", addr, pc)
	}
	return conn, nil
}

func describeListen(target string) string {
	if target == "" {
		return "0.0.0.0"
	}
	return target
}

// ParsePorts reads the test-only "67,4011" style override of the two
// ProxyDHCP listen ports. An empty string yields the defaults (0, 0).
func ParsePorts(s string) (port67, port4011 int, err error) {
	if strings.TrimSpace(s) == "" {
		return 0, 0, nil
	}
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("proxydhcp ports %q: want two comma-separated ports", s)
	}
	ports := make([]int, 2)
	for i, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || v < 1 || v > 65535 {
			return 0, 0, fmt.Errorf("proxydhcp ports %q: invalid port %q", s, p)
		}
		ports[i] = v
	}
	if ports[0] == ports[1] {
		return 0, 0, fmt.Errorf("proxydhcp ports %q: ports must differ", s)
	}
	return ports[0], ports[1], nil
}
