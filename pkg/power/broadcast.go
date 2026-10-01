package power

import (
	"net"
	"syscall"
)

func enableBroadcast(conn net.PacketConn) error {
	udp, ok := conn.(*net.UDPConn)
	if !ok {
		return nil
	}
	raw, err := udp.SyscallConn()
	if err != nil {
		return err
	}
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
	}); err != nil {
		return err
	}
	return sockErr
}
