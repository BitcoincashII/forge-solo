package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// installedProgramsOS lists the processes, other than this one, whose program is in installDir.
// Copied from windows/launcher/leftovers_windows.go.
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
			// A program started from a short (8.3) path names itself in that form: compared in its
			// long form, it is still this install's.
			if path := strings.ToLower(longPath(windows.UTF16ToString(buf[:n]))); strings.HasPrefix(path, dir) {
				out = append(out, runningProgram{int(e.ProcessID), filepath.Base(path)})
			}
		}
		_ = windows.CloseHandle(h)
	}
	return out
}

// longPath is path's long form: a process started through a short path can name its program in
// that form. Copied from windows/launcher/pgpath_windows.go.
func longPath(path string) string {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return path
	}
	buf := make([]uint16, 1024)
	n, err := windows.GetLongPathName(p, &buf[0], uint32(len(buf)))
	if err != nil || int(n) > len(buf) || n == 0 {
		return path
	}
	return windows.UTF16ToString(buf[:n])
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
