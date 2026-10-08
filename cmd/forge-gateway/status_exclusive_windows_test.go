package main

import (
	"net"
	"syscall"
	"testing"
)

// The status page's port is the gateway's alone: its listener takes Windows's exclusive address
// use, so that no other program can bind the same address and port beside it, answer the page and
// ask for the settings password.
//
// A bind beside it does not show the option: Windows 11 refuses a second listener on the same
// address beside a plain listener too, and allows one on every address (0.0.0.0) beside an
// exclusive one, whose connections to 127.0.0.1, where the page is, still reach the page. So the
// option is read from the socket.
func TestStatusPortIsExclusive(t *testing.T) {
	l, err := listenStatus("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	rc, err := l.(*net.TCPListener).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var on int
	var gerr error
	if err := rc.Control(func(fd uintptr) {
		on, gerr = syscall.GetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, ^syscall.SO_REUSEADDR)
	}); err != nil || gerr != nil {
		t.Fatalf("the status page's socket options cannot be read: %v %v", err, gerr)
	}
	if on == 0 {
		t.Error("GW-STATUS-EXCLUSIVE: the status page listens without exclusive address use (SO_EXCLUSIVEADDRUSE)")
	}
	addr := l.Addr().String()
	if other, err := net.Listen("tcp", addr); err == nil {
		other.Close()
		t.Errorf("GW-STATUS-EXCLUSIVE-BIND: another listener bound %s beside the status page", addr)
	}
}
