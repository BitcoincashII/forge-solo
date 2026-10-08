package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Who holds a port the gateway needs (portError.holder).
const (
	holderSolo    = "forge-solo"            // Forge Solo, whose TIDES mode is the same gateway
	holderGateway = "forge-gateway"         // another forge-gateway.exe: a Command Prompt's, or another account's
	holderService = "forge-gateway-service" // a Forge Gateway Windows service (1.0.0's install)
	holderOther   = "other"
)

// checkPorts fails when a port the gateway listens on cannot be had: the miners' port and the
// status page's. The gateway would exit at once without either (exit code 4).
//
// It never listens on a public address itself: Windows Firewall asks the person at the screen to
// let through a program that does, and forge-gateway-tray.exe has no rule (forge-gateway.exe has).
// It reads the system's TCP listener tables instead, IPv4 and IPv6, and takes a listener on any of
// the port's addresses for another program's.
func checkPorts() *portError {
	for _, p := range []struct{ port, what string }{{stratumPort, "the miner port"}, {statusPort, "the status page"}} {
		if err := portTaken(p.port, p.what); err != nil {
			return err
		}
	}
	return nil
}

// A portError is a port Forge Gateway cannot have: another program listens on it, or Windows keeps
// it for itself (a range reserved for Hyper-V, WSL or Docker), and why. holder is who listens on it
// (holderSolo, ...).
type portError struct {
	port, what string
	cause      error
	reserved   bool
	holder     string
}

func (e *portError) Error() string { return e.why() + " (" + e.cause.Error() + ")" }
func (e *portError) Unwrap() error { return e.cause }

// why is what launcher.log says.
func (e *portError) why() string {
	if e.reserved {
		return "Windows keeps port " + e.port + ", " + e.what + ", for itself"
	}
	switch e.holder {
	case holderSolo:
		return "Forge Solo uses port " + e.port + ", " + e.what
	case holderService:
		return "the ForgeGateway Windows service uses port " + e.port + ", " + e.what
	case holderGateway:
		return gatewayExe + " uses port " + e.port + ", " + e.what
	}
	return "another program uses port " + e.port + ", " + e.what
}

// trayWhy is why in the few words the tray has room for.
func (e *portError) trayWhy() string {
	if e.reserved {
		return "Windows keeps port " + e.port + " for itself"
	}
	switch e.holder {
	case holderSolo:
		return "Forge Solo uses port " + e.port
	case holderService:
		return "its Windows service uses port " + e.port
	case holderGateway:
		return gatewayExe + " uses port " + e.port
	}
	return "another program uses port " + e.port
}

// todo is what to do to free the port, held by one of ours, and what goes after it before more is
// said; "" for another program, or a port Windows keeps, which only waiting frees.
func (e *portError) todo() (what, sep string) {
	if e.reserved {
		return "", ""
	}
	switch e.holder {
	case holderSolo:
		return "Forge Solo mines on this computer already, and its TIDES mode is the same gateway, built in: run one of the two. " +
			"To run Forge Gateway, quit Forge Solo (right-click its tray icon, then Quit Forge Solo)", ", "
	case holderService:
		return "it is a Forge Gateway installed as a service, 1.0.0's say, and it starts with Windows. " +
			"Run Forge Gateway's installer again and choose Yes when it offers to remove the service, " +
			"or in a Command Prompt run as administrator run sc stop ForgeGateway, then sc delete ForgeGateway", "; "
	case holderGateway:
		return "another Forge Gateway runs on this computer, in a Command Prompt or for another Windows account: stop it", ", "
	}
	return "", ""
}

// advice is what launcher.log says to do after a start that failed on the port, Try Again last.
func (e *portError) advice() string {
	if todo, sep := e.todo(); todo != "" {
		return todo + sep + tryAgainAdvice
	}
	if e.reserved {
		return tryAgainAdvice
	}
	return closeItAdvice
}

// triesAgain ends what launcher.log says when the gateway exited on a port: it is started again
// with no click, so it starts once the port is free.
const triesAgain = "Forge Gateway tries again by itself"

// byItself is what launcher.log says to do when the gateway exited on the port.
func (e *portError) byItself() string {
	if todo, _ := e.todo(); todo != "" {
		return todo + "; " + triesAgain
	}
	if e.reserved {
		return triesAgain
	}
	return "close that program; " + triesAgain
}

// listenProbe listens on an address to see whether the system lets a program have it (a stand-in
// in the tests).
var listenProbe = net.Listen

// portTaken says why port cannot be had, or nil. Copied from windows/launcher/public_ports.go, with
// the holder named.
func portTaken(port, what string) *portError {
	n, _ := strconv.Atoi(port)
	for _, l := range tcpListeners() {
		if l.port == n {
			holder := "process " + strconv.Itoa(l.pid)
			name := processName(l.pid)
			if name == "" {
				name = exeName(l.pid)
			}
			if name != "" {
				holder = name + ", " + holder
			}
			return &portError{port, what, fmt.Errorf("%s listens on it at %s", holder, l.ip), false, holderOf(l.pid)}
		}
	}
	// A port Windows keeps for itself has no listener, and no program may take it. Tried on this
	// machine's own addresses, which the firewall does not ask about.
	pl, err := listenProbe("tcp4", "127.0.0.1:"+port)
	if err != nil {
		if errors.Is(err, accessDenied) {
			err = fmt.Errorf("%w; Windows may reserve it: netsh int ipv4 show excludedportrange protocol=tcp", err)
			return &portError{port, what, err, true, ""}
		}
		return &portError{port, what, err, false, holderOther}
	}
	_ = pl.Close()
	// Windows can keep a port for IPv6 alone, and the gateway listens on IPv6 too. Only that
	// refusal counts here: IPv6 may be turned off on this PC.
	pl, err = listenProbe("tcp6", "[::1]:"+port)
	if err != nil {
		if errors.Is(err, accessDenied) {
			err = fmt.Errorf("%w; Windows may reserve it: netsh int ipv6 show excludedportrange protocol=tcp", err)
			return &portError{port, what, err, true, ""}
		}
		return nil
	}
	_ = pl.Close()
	return nil
}

// holderOf says who process pid is, of those that could hold the gateway's ports.
func holderOf(pid int) string {
	// A service runs as LocalSystem: a standard account cannot read its program's path.
	if s := gatewayServicePID(); s != 0 && s == pid {
		return holderService
	}
	path := processPath(pid)
	// Forge Solo installs to %LOCALAPPDATA%\Programs\ForgeSolo.
	if path != "" && strings.EqualFold(winBase(winDir(path)), "ForgeSolo") {
		return holderSolo
	}
	name := exeName(pid)
	if path != "" {
		name = winBase(path)
	}
	if strings.EqualFold(name, gatewayExe) {
		return holderGateway
	}
	// Another account's Forge Solo, whose path this account cannot read.
	if path == "" && forgeSoloRuns() {
		return holderSolo
	}
	return holderOther
}

// winDir and winBase are filepath.Dir and filepath.Base for a Windows path, on any system.
func winDir(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[:i]
	}
	return ""
}
func winBase(p string) string { return p[strings.LastIndexAny(p, `\/`)+1:] }

// A tcpListener is one row of a Windows TCP listener table: the local address and port, and the
// process listening.
type tcpListener struct {
	ip   net.IP
	port int
	pid  int
}

// The operating system's part of finding who holds a port (stand-ins in the tests).
var (
	tcpListeners      = tcpListenersOS      // every TCP listener, IPv4 and IPv6
	processName       = processNameOS       // a process's program file name; "" when it cannot be had
	processPath       = processPathOS       // a process's program, its full path; "" when it cannot be had
	exeName           = exeNameOS           // a process's program file name, for any process; "" when there is none
	forgeSoloRuns     = forgeSoloRunsOS     // whether Forge Solo runs on this PC, for any account
	gatewayServicePID = gatewayServicePIDOS // the running ForgeGateway service's process; 0 when none runs
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
