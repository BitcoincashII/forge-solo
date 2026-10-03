package main

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                     = windows.NewLazySystemDLL("user32.dll")
	pRegisterClassExW          = user32.NewProc("RegisterClassExW")
	pCreateWindowExW           = user32.NewProc("CreateWindowExW")
	pDefWindowProcW            = user32.NewProc("DefWindowProcW")
	pGetMessageW               = user32.NewProc("GetMessageW")
	pDispatchMessageW          = user32.NewProc("DispatchMessageW")
	pShutdownBlockReasonCreate = user32.NewProc("ShutdownBlockReasonCreate")
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
// Once the session ends a program has about 5 s, and the tray only stopped everything then, one
// service after another: the nodes were killed before they had written their chainstate, and a
// killed node has to sync again. So when Windows asks whether the session may end, the window
// says not yet, with a reason Windows shows the user, and starts the stop. That exits Forge Solo
// once everything has stopped, and Windows then carries on.
func watchSessionEnd() {
	go func() {
		// A window belongs to the thread that made it, and only that thread reads its messages.
		runtime.LockOSThread()
		const wmQueryEndSession, wmEndSession = 0x0011, 0x0016
		reason, _ := windows.UTF16PtrFromString("Closing the BCH2 node safely, so it does not have to sync again.")
		proc := func(hwnd windows.Handle, msg uint32, wParam, lParam uintptr) uintptr {
			switch msg {
			case wmQueryEndSession:
				pShutdownBlockReasonCreate.Call(uintptr(hwnd), uintptr(unsafe.Pointer(reason)))
				go shutdown()
				return 0 // not yet
			case wmEndSession:
				if wParam != 0 {
					shutdown() // the session ends now, whatever is answered: carry on stopping
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
			return
		}
		hwnd, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)),
			0, 0, 0, 0, 0, 0, 0, uintptr(inst), 0)
		if hwnd == 0 {
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
