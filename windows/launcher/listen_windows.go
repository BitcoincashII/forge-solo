package main

import (
	"context"
	"net"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// listenExclusive is net.Listen with Windows's exclusive address use, so that no other program can
// bind the same port beside it and take its connections (internal/netlisten does the same for the
// services).
func listenExclusive(network, addr string) (net.Listener, error) {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, ^syscall.SO_REUSEADDR, 1)
		}); err != nil {
			return err
		}
		return serr
	}}
	return lc.Listen(context.Background(), network, addr)
}

// accessDenied is the error Windows gives a program that may not have a port (WSAEACCES).
var accessDenied error = windows.WSAEACCES

const afInet, afInet6, tcpTableOwnerPIDListener = 2, 23, 3

// tcpTable is the TCP listener table of one address family (afInet or afInet6), as
// GetExtendedTcpTable gives it with TCP_TABLE_OWNER_PID_LISTENER; nil when it cannot be read.
func tcpTable(af uintptr) []byte {
	size := uint32(16 << 10)
	for try := 0; try < 4; try++ {
		buf := make([]byte, size)
		r, _, _ := pGetExtendedTcpTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, af, tcpTableOwnerPIDListener, 0)
		switch r {
		case 0:
			return buf
		case uintptr(windows.ERROR_INSUFFICIENT_BUFFER):
			size += 4096 // what it needs now, and room for listeners opened meanwhile
		default:
			return nil
		}
	}
	return nil
}

// tcpListenersOS lists every TCP listener on this machine, IPv4 and IPv6.
func tcpListenersOS() []tcpListener {
	return append(parseTCPTable(tcpTable(afInet), false), parseTCPTable(tcpTable(afInet6), true)...)
}

// processNameOS is the file name of the program process pid runs; "" when it cannot be had (the
// System process, say, which holds ports for Windows's own web server).
func processNameOS(pid int) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 4096)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return ""
	}
	return filepath.Base(windows.UTF16ToString(buf[:n]))
}
