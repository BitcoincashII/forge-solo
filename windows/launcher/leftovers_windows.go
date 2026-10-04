package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iphlpapi             = windows.NewLazySystemDLL("iphlpapi.dll")
	pGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
)

// installedProgramsOS lists the processes, other than this one, whose program is in installDir.
func installedProgramsOS() []runningProgram {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	dir := strings.ToLower(longPath(filepath.Clean(installDir))) + `\`
	var out []runningProgram
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if int(e.ProcessID) == os.Getpid() {
			continue
		}
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, e.ProcessID)
		if err != nil {
			continue
		}
		buf := make([]uint16, 4096)
		n := uint32(len(buf))
		if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil {
			// The bundled PostgreSQL runs from its short (8.3) path when the long one has characters
			// it cannot take (pgPath): compared in its long form, it is still this install's.
			if path := strings.ToLower(longPath(windows.UTF16ToString(buf[:n]))); strings.HasPrefix(path, dir) {
				out = append(out, runningProgram{int(e.ProcessID), filepath.Base(path)})
			}
		}
		_ = windows.CloseHandle(h)
	}
	return out
}

// loopbackPortsOS lists the ports pid listens on at 127.0.0.1, from the IPv4 TCP listener table.
func loopbackPortsOS(pid int) []int {
	var ports []int
	for _, l := range parseTCPTable(tcpTable(afInet), false) {
		if l.pid == pid && l.ip.Equal(net.IPv4(127, 0, 0, 1)) {
			ports = append(ports, l.port)
		}
	}
	return ports
}

func waitPIDOS(pid int, d time.Duration) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return true // already gone
	}
	defer windows.CloseHandle(h)
	ev, _ := windows.WaitForSingleObject(h, uint32(d.Milliseconds()))
	return ev == windows.WAIT_OBJECT_0
}

func killPIDOS(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}
