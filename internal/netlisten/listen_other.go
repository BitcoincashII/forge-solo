//go:build !windows

package netlisten

import "net"

func listenConfig() net.ListenConfig { return net.ListenConfig{} }
