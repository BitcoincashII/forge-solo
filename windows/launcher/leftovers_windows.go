package main

import (
	"encoding/binary"
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

// loopbackPortsOS lists the ports pid listens on at 127.0.0.1, from the IPv4 TCP listener table
// (GetExtendedTcpTable, TCP_TABLE_OWNER_PID_LISTENER). Each row is six DWORDs: state, local
// address (in_addr), local port (network byte order in the low 16 bits), remote address, remote
// port, owning process id.
func loopbackPortsOS(pid int) []int {
	const afInet, ownerPIDListener = 2, 3
	var size uint32
	pGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, afInet, ownerPIDListener, 0)
	if size == 0 {
		return nil
	}
	buf := make([]byte, size+4096) // room for listeners opened meanwhile
	size = uint32(len(buf))
	if r, _, _ := pGetExtendedTcpTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, afInet, ownerPIDListener, 0); r != 0 {
		return nil
	}
	var ports []int
	n := binary.LittleEndian.Uint32(buf)
	for i := uint32(0); i < n && 4+(i+1)*24 <= uint32(len(buf)); i++ {
		row := buf[4+i*24 : 4+(i+1)*24]
		if int(binary.LittleEndian.Uint32(row[20:24])) == pid && row[4] == 127 && row[5] == 0 && row[6] == 0 && row[7] == 1 {
			ports = append(ports, int(row[8])<<8|int(row[9]))
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
