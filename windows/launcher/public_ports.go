package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
)

// publicPorts are the fixed ports other machines reach Forge Solo on (a variable so that the tests
// can use free ones). Another program holding a required one stops mining altogether: the miner,
// or the BCH2 node, exits at once without it, while the tray said "running".
var publicPorts = []struct {
	port, what string
	required   bool
}{{minerPort, "the miner port", true}, {bch2P2P, "the BCH2 node's peer port", true},
	{rentalPort, rentalWhat, false}, {aux1175P2P, "the 1175 node's peer port", false}}

// rentalWhat is what the rental port is called, as Linux and the dashboard call it.
const rentalWhat = "the rental port"

// auxNoPeers is set when another program holds the 1175 node's peer port: the node then runs
// without taking incoming peers (listen=0). It still syncs over the peers it reaches, and merge
// mining goes on. With listen=1 it exits at once against a program that does not share the port,
// and beside another 1175 node or wallet, which does, the two share the incoming peers.
var auxNoPeers bool

// checkPublicPorts fails when a required public port cannot be had. Without an optional one Forge
// Solo runs all the same: rentals have no port of their own, which the tray, launcher.log and the
// dashboard say, or the 1175 node takes no incoming peers.
//
// It never listens on a public address itself: Windows Firewall asks the person at the screen to
// let through a program that does, and forge-solo.exe has no rule (the miner and the nodes do). It
// reads the system's TCP listener tables instead, IPv4 and IPv6, and takes a listener on any of the
// port's addresses for another program's: Windows lets the miner's own listen (one socket for IPv6
// and IPv4) succeed beside another program's IPv4-only one, and then gives every IPv4 connection to
// the other program.
func checkPublicPorts() *portError {
	auxNoPeers = false
	setRunningNote("rentals", "")
	for _, p := range publicPorts {
		err := portTaken(p.port, p.what)
		switch {
		case err == nil:
		case p.required:
			return err
		case p.port == aux1175P2P:
			auxNoPeers = true
			logf("%s: the 1175 node runs without incoming peers, and merge mining goes on (%v)", err.why(), err.cause)
		case p.what == rentalWhat && err.reserved:
			setRunningNote("rentals", noteRentalsReserved)
			logf("%s: rentals have no port of their own until Windows lets it go and you restart Forge Solo (%v)", err.why(), err.cause)
		case p.what == rentalWhat:
			setRunningNote("rentals", noteNoRentals)
			logf("%s: rentals have no port of their own until you stop it and restart Forge Solo (%v)", err.why(), err.cause)
		default:
			logf("%s: that part is left out (%v)", err.why(), err.cause)
		}
	}
	return nil
}

// A portError is a public port Forge Solo cannot have: another program listens on it, or Windows
// keeps it for itself (a range reserved for Hyper-V, WSL or Docker), and why.
type portError struct {
	port, what string
	cause      error
	reserved   bool
}

func (e *portError) Error() string { return e.why() + " (" + e.cause.Error() + ")" }
func (e *portError) Unwrap() error { return e.cause }

// why is what launcher.log says.
func (e *portError) why() string {
	if e.reserved {
		return "Windows keeps port " + e.port + ", " + e.what + ", for itself"
	}
	return "another program uses port " + e.port + ", " + e.what
}

// trayWhy is why in the few words the tray has room for.
func (e *portError) trayWhy() string {
	if e.reserved {
		return "Windows keeps port " + e.port + " for itself"
	}
	return "another program uses port " + e.port
}

// listenProbe listens on an address to see whether the system lets a program have it (a stand-in
// in the tests).
var listenProbe = net.Listen

// portTaken says why port cannot be had, or nil.
func portTaken(port, what string) *portError {
	n, _ := strconv.Atoi(port)
	for _, l := range tcpListeners() {
		if l.port == n {
			holder := "process " + strconv.Itoa(l.pid)
			if name := processName(l.pid); name != "" {
				holder = name + ", " + holder
			}
			return &portError{port, what, fmt.Errorf("%s listens on it at %s", holder, l.ip), false}
		}
	}
	// A port Windows keeps for itself has no listener, and no program may take it. Tried on this
	// machine's own addresses, which the firewall does not ask about.
	pl, err := listenProbe("tcp4", "127.0.0.1:"+port)
	if err != nil {
		if errors.Is(err, accessDenied) {
			err = fmt.Errorf("%w; Windows may reserve it: netsh int ipv4 show excludedportrange protocol=tcp", err)
			return &portError{port, what, err, true}
		}
		return &portError{port, what, err, false}
	}
	_ = pl.Close()
	// Windows can keep a port for IPv6 alone, and the miner and the nodes listen on IPv6 too. Only
	// that refusal counts here: IPv6 may be turned off on this PC.
	pl, err = listenProbe("tcp6", "[::1]:"+port)
	if err != nil {
		if errors.Is(err, accessDenied) {
			err = fmt.Errorf("%w; Windows may reserve it: netsh int ipv6 show excludedportrange protocol=tcp", err)
			return &portError{port, what, err, true}
		}
		return nil
	}
	_ = pl.Close()
	return nil
}

// A tcpListener is one row of a Windows TCP listener table: the local address and port, and the
// process listening.
type tcpListener struct {
	ip   net.IP
	port int
	pid  int
}

// The operating system's part of finding who holds a port (stand-ins in the tests).
var (
	tcpListeners = tcpListenersOS // every TCP listener, IPv4 and IPv6
	processName  = processNameOS  // a process's program file name; "" when it cannot be had
)

// parseTCPTable reads a table GetExtendedTcpTable gave with TCP_TABLE_OWNER_PID_LISTENER: a DWORD
// row count, then the rows. An IPv4 row (MIB_TCPROW_OWNER_PID) is six DWORDs: state, local address,
// local port (network byte order in the low 16 bits), remote address, remote port, owning process.
// An IPv6 row (MIB_TCP6ROW_OWNER_PID) is 56 bytes: local address (16), scope (4), local port (4),
// remote address (16), scope (4), remote port (4), state (4), owning process (4).
func parseTCPTable(buf []byte, v6 bool) []tcpListener {
	if len(buf) < 4 {
		return nil
	}
	size, addrAt, addrLen, portAt, pidAt := 24, 4, 4, 8, 20
	if v6 {
		size, addrAt, addrLen, portAt, pidAt = 56, 0, 16, 20, 52
	}
	n := int(binary.LittleEndian.Uint32(buf))
	var out []tcpListener
	for i := 0; i < n && 4+(i+1)*size <= len(buf); i++ {
		row := buf[4+i*size : 4+(i+1)*size]
		out = append(out, tcpListener{
			ip:   net.IP(append([]byte(nil), row[addrAt:addrAt+addrLen]...)),
			port: int(row[portAt])<<8 | int(row[portAt+1]),
			pid:  int(binary.LittleEndian.Uint32(row[pidAt : pidAt+4])),
		})
	}
	return out
}
