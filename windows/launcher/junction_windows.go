package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// A junction (a mount point, as mklink /J makes) is a folder that leads to another folder. Any
// account may make one, without administrator rights. os.Remove removes the junction only.
const (
	fsctlSetReparsePoint   = 0x000900A4
	ioReparseTagMountPoint = 0xA0000003
)

// makeJunction makes link, which must not exist, a junction to the folder target.
var makeJunction = func(link, target string) error {
	abs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	// REPARSE_DATA_BUFFER for IO_REPARSE_TAG_MOUNT_POINT: ReparseTag, ReparseDataLength, Reserved,
	// then the offsets and lengths of the substitute name in NT form (\??\C:\...) and of the print
	// name, then both names, each ending in a NUL.
	sub := utf16.Encode([]rune(`\??\` + abs))
	prt := utf16.Encode([]rune(abs))
	names := append(append(append(sub, 0), prt...), 0)
	buf := make([]byte, 16+2*len(names))
	le := binary.LittleEndian
	le.PutUint32(buf[0:], ioReparseTagMountPoint)
	le.PutUint16(buf[4:], uint16(8+2*len(names)))
	le.PutUint16(buf[8:], 0)
	le.PutUint16(buf[10:], uint16(2*len(sub)))
	le.PutUint16(buf[12:], uint16(2*(len(sub)+1)))
	le.PutUint16(buf[14:], uint16(2*len(prt)))
	for i, u := range names {
		le.PutUint16(buf[16+2*i:], u)
	}

	if err := os.Mkdir(link, 0o755); err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(link)
	if err != nil {
		os.Remove(link)
		return err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		os.Remove(link)
		return err
	}
	var n uint32
	err = windows.DeviceIoControl(h, fsctlSetReparsePoint, &buf[0], uint32(len(buf)), nil, 0, &n, nil)
	windows.CloseHandle(h)
	if err != nil {
		os.Remove(link)
		return err
	}
	return nil
}
