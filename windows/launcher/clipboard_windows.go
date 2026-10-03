package main

import (
	"errors"
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
)

// copyText puts s on the clipboard as text (CF_UNICODETEXT).
func copyText(s string) error {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return err
	}
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
	size := uintptr(len(u) * 2)
	const gmemMoveable, cfUnicodeText = 0x0002, 13
	h, _, err := pGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return err
	}
	p, _, err := pGlobalLock.Call(h)
	if p == 0 {
		pGlobalFree.Call(h)
		return err
	}
	pRtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&u[0])), size)
	pGlobalUnlock.Call(h)
	if r, _, err := pSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		pGlobalFree.Call(h)
		return err
	}
	return nil // the clipboard owns the memory now
}
