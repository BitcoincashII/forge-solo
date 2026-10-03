package main

import (
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32            = windows.NewLazySystemDLL("user32.dll")
	pRegisterClassExW = user32.NewProc("RegisterClassExW")
	pCreateWindowExW  = user32.NewProc("CreateWindowExW")
	pDefWindowProcW   = user32.NewProc("DefWindowProcW")
	pGetMessageW      = user32.NewProc("GetMessageW")
	pDispatchMessageW = user32.NewProc("DispatchMessageW")

	kernel32                      = windows.NewLazySystemDLL("kernel32.dll")
	pSetProcessShutdownParameters = kernel32.NewProc("SetProcessShutdownParameters")
)

type wndClassExW struct {
	Size, Style                        uint32
	WndProc                            uintptr
	ClsExtra, WndExtra                 int32
	Instance, Icon, Cursor, Background windows.Handle
	MenuName, ClassName                *uint16
	IconSm                             windows.Handle
}

type msgW struct {
	Hwnd     windows.Handle
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       struct{ X, Y int32 }
	LPrivate uint32
}

// watchSessionEnd hears Windows end the session (shut down, restart, sign out, an update's
// restart) through a window of its own that is never shown. A message-only window would not be
// told.
//
// A program with no visible window may not hold the session open: Windows ends it at once if it
// answers "not yet", and after 5 s in each of the two messages otherwise ("Shutdown Changes for
// Windows Vista"). So Forge Solo asks to be told first, before other programs (the nodes among
// them), stops everything together, and answers yes while Windows still waits on either of the
// messages. The nodes write their chainstate in that time; killed before it, a node has to sync again.
func watchSessionEnd() {
	// 0x3FF: the first of the levels for applications. Everything starts at 0x280.
	pSetProcessShutdownParameters.Call(0x3FF, 0)
	go func() {
		// A window belongs to the thread that made it, and only that thread reads its messages.
		runtime.LockOSThread()
		const wmQueryEndSession, wmEndSession = 0x0011, 0x0016
		proc := func(hwnd windows.Handle, msg uint32, wParam, lParam uintptr) uintptr {
			switch msg {
			case wmQueryEndSession:
				logf("Windows is ending the session (flags %#x): stopping everything at once", lParam)
				sessionEnding.Store(true)
				go shutdown()
				// Each of the two messages may take up to 5 s. Spending up to 4 s of this one on the
				// stop, which ends the process when it is done, gives the nodes about 9 s instead of 5.
				if !waitStopped(4 * time.Second) {
					logf("still stopping after 4 s: told Windows to go on")
				}
				return 1
			case wmEndSession:
				if wParam != 0 {
					logf("Windows ends the session now")
					shutdown() // waits for the stop already under way; it exits when done
				}
				return 0
			}
			r, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
			return r
		}
		var inst windows.Handle
		if windows.GetModuleHandleEx(0, nil, &inst) != nil {
			return
		}
		class, _ := windows.UTF16PtrFromString("ForgeSoloSession")
		title, _ := windows.UTF16PtrFromString("Forge Solo")
		wc := wndClassExW{WndProc: windows.NewCallback(proc), Instance: inst, ClassName: class}
		wc.Size = uint32(unsafe.Sizeof(wc))
		if r, _, _ := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
			logf("session watch: no window class")
			return
		}
		hwnd, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)),
			0, 0, 0, 0, 0, 0, 0, uintptr(inst), 0)
		if hwnd == 0 {
			logf("session watch: no window")
			return
		}
		var m msgW
		for {
			if r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0); int32(r) <= 0 {
				return
			}
			pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
	}()
}
