package main

import (
	"errors"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	pOpenClipboard    = user32.NewProc("OpenClipboard")
	pCloseClipboard   = user32.NewProc("CloseClipboard")
	pEmptyClipboard   = user32.NewProc("EmptyClipboard")
	pSetClipboardData = user32.NewProc("SetClipboardData")
	pGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	pGlobalLock       = kernel32.NewProc("GlobalLock")
	pGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	pGlobalFree       = kernel32.NewProc("GlobalFree")
	pRtlMoveMemory    = kernel32.NewProc("RtlMoveMemory")

	pRegisterClipboardFormatW = user32.NewProc("RegisterClipboardFormatW")
)

// The formats that keep what is copied out of Windows' clipboard history (Win+V) and its cloud
// clipboard, which would otherwise keep the Settings password, and send it to the account's other
// devices: "Cloud Clipboard and Clipboard History Formats" in Microsoft's clipboard documentation.
// The first two take a DWORD 0; the third, any data.
var privateFormats = []string{"CanIncludeInClipboardHistory", "CanUploadToCloudClipboard", "ExcludeClipboardContentFromMonitorProcessing"}

// copyText puts s on the clipboard as text (CF_UNICODETEXT), for pasting only.
func copyText(s string) error {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return err
	}
	// The clipboard is opened by a thread, and only that thread may use and close it. A goroutine can
	// move between threads at any call; then the clipboard stayed open, for every program, until
	// Forge Solo exited.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var opened uintptr
	for i := 0; i < 10 && opened == 0; i++ { // another program may hold the clipboard a moment
		if opened, _, _ = pOpenClipboard.Call(0); opened == 0 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if opened == 0 {
		return errors.New("the clipboard is in use")
	}
	defer pCloseClipboard.Call()
	pEmptyClipboard.Call()
	for _, name := range privateFormats {
		n, _ := windows.UTF16PtrFromString(name)
		if f, _, _ := pRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(n))); f != 0 {
			zero := uint32(0)
			_ = setClipboard(f, unsafe.Pointer(&zero), 4)
		}
	}
	const cfUnicodeText = 13
	return setClipboard(cfUnicodeText, unsafe.Pointer(&u[0]), uintptr(len(u)*2))
}

// setClipboard puts size bytes from data on the open clipboard in format f.
func setClipboard(f uintptr, data unsafe.Pointer, size uintptr) error {
	const gmemMoveable = 0x0002
	h, _, err := pGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return err
	}
	p, _, err := pGlobalLock.Call(h)
	if p == 0 {
		pGlobalFree.Call(h)
		return err
	}
	pRtlMoveMemory.Call(p, uintptr(data), size)
	pGlobalUnlock.Call(h)
	if r, _, err := pSetClipboardData.Call(f, h); r == 0 {
		pGlobalFree.Call(h)
		return err
	}
	return nil // the clipboard owns the memory now
}
