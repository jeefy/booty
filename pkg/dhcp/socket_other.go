//go:build !unix

package dhcp

import (
	"errors"
	"syscall"
)

func controlFunc(iface string) func(network, address string, c syscall.RawConn) error {
	return func(_, _ string, _ syscall.RawConn) error {
		if iface != "" {
			return errors.New("proxydhcp: binding to an interface is only supported on unix")
		}
		return nil
	}
}
