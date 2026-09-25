//go:build unix

package dhcp

import (
	"syscall"

	"github.com/insomniacslk/dhcp/dhcpv4"
)

func controlFunc(iface string) func(network, address string, c syscall.RawConn) error {
	return func(_, _ string, rc syscall.RawConn) error {
		var opErr error
		err := rc.Control(func(fd uintptr) {
			if opErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1); opErr != nil {
				return
			}
			if opErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); opErr != nil {
				return
			}
			if iface != "" {
				opErr = dhcpv4.BindToInterface(int(fd), iface)
			}
		})
		if err != nil {
			return err
		}
		return opErr
	}
}
