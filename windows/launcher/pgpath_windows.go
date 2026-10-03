package main

import "golang.org/x/sys/windows"

// shortPath is path's short (8.3) form, as Windows keeps it for an existing file; the same path when
// the drive keeps no short names.
var shortPath = func(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, 1024)
	n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil {
		return "", err
	}
	if int(n) > len(buf) {
		buf = make([]uint16, n)
		if n, err = windows.GetShortPathName(p, &buf[0], uint32(len(buf))); err != nil {
			return "", err
		}
	}
	return windows.UTF16ToString(buf[:n]), nil
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
