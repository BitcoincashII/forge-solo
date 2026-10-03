package netlisten

import (
	"net"
	"syscall"
)

// soExclusiveAddrUse is SO_EXCLUSIVEADDRUSE: ~SO_REUSEADDR in Winsock.
const soExclusiveAddrUse = ^syscall.SO_REUSEADDR

func listenConfig() net.ListenConfig {
	return net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, soExclusiveAddrUse, 1)
		}); err != nil {
			return err
		}
		return serr
	}}
}
