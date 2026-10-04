package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// shortName is the short (8.3) name the drive keeps for the existing file or folder at path, as
// FindFirstFile reads it; "" when it keeps none (8.3 names off, or a name that is one already).
var shortName = func(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	var fd windows.Win32finddata
	h, err := windows.FindFirstFile(p, &fd)
	if err != nil {
		return "", err
	}
	_ = windows.FindClose(h)
	return windows.UTF16ToString(fd.AlternateFileName[:]), nil
}

var pWideCharToMultiByte = kernel32.NewProc("WideCharToMultiByte")

// wcNoBestFitChars is WC_NO_BEST_FIT_CHARS: a character the code page lacks counts as lacking, not
// as its look-alike. cpUTF8 is the UTF-8 code page.
const wcNoBestFitChars, cpUTF8 = 0x400, 65001

// codePageHolds reports whether s converts to the system's ANSI code page with no character lost
// or replaced by a look-alike (a Polish l must not become a plain l, which names another folder).
var codePageHolds = func(s string) bool {
	acp := windows.GetACP()
	if acp == cpUTF8 {
		return true // the programs then read their arguments in UTF-8: every name converts
	}
	u, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return false
	}
	var usedDefault int32
	n, _, _ := pWideCharToMultiByte.Call(uintptr(acp), wcNoBestFitChars, uintptr(unsafe.Pointer(u)), ^uintptr(0),
		0, 0, 0, uintptr(unsafe.Pointer(&usedDefault)))
	return n != 0 && usedDefault == 0
}

// longPath is path's long form: a process started through a short path can name its program in
// that form.
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
