package main

import (
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The listener tables, the error for a port Windows keeps, and processNameOS are copied from
// windows/launcher/listen_windows.go and leftovers_windows.go.

var (
	iphlpapi             = windows.NewLazySystemDLL("iphlpapi.dll")
	pGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
)

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
	if p := processPathOS(pid); p != "" {
		return filepath.Base(p)
	}
	return ""
}

// processPathOS is the full path of the program process pid runs; "" when it cannot be had: a
// standard account cannot open another account's process, nor a service's.
func processPathOS(pid int) string {
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
	return windows.UTF16ToString(buf[:n])
}

// exeNameOS is the file name of the program process pid runs, from a snapshot of the processes,
// which Windows gives for every process, another account's and a service's included; "" when there
// is no such process.
func exeNameOS(pid int) string {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if int(e.ProcessID) == pid {
			return windows.UTF16ToString(e.ExeFile[:])
		}
	}
	return ""
}

// forgeSoloRunsOS reports whether Forge Solo runs on this PC, for this account or another: its
// launcher holds the mutex Global\ForgeSoloRunning while it runs. Refused access means it exists,
// as the installers take it (forge-solo.iss, MutexHeld).
func forgeSoloRunsOS() bool {
	name, err := windows.UTF16PtrFromString(`Global\ForgeSoloRunning`)
	if err != nil {
		return false
	}
	h, err := windows.OpenMutex(windows.SYNCHRONIZE, false, name)
	if err == nil {
		_ = windows.CloseHandle(h)
		return true
	}
	return err == windows.ERROR_ACCESS_DENIED
}

// gatewayService is the Windows service forge-gateway.exe install makes (serviceName in
// cmd/forge-gateway/service_windows.go). Forge Gateway 1.0.0's guide set it up, and it starts with
// Windows as LocalSystem, holding the gateway's ports.
const gatewayService = "ForgeGateway"

// gatewayServicePIDOS is the process id of the running ForgeGateway service; 0 when there is none,
// or it does not run. A standard account may ask: Windows lets interactive users query a service's
// status.
func gatewayServicePIDOS() int {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0
	}
	defer windows.CloseServiceHandle(m)
	name, err := windows.UTF16PtrFromString(gatewayService)
	if err != nil {
		return 0
	}
	s, err := windows.OpenService(m, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return 0
	}
	defer windows.CloseServiceHandle(s)
	var st windows.SERVICE_STATUS_PROCESS
	var need uint32
	if windows.QueryServiceStatusEx(s, windows.SC_STATUS_PROCESS_INFO, (*byte)(unsafe.Pointer(&st)), uint32(unsafe.Sizeof(st)), &need) != nil {
		return 0
	}
	if st.CurrentState == windows.SERVICE_STOPPED {
		return 0
	}
	return int(st.ProcessId)
}
