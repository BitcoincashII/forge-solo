//go:build windows

package pgmigrate

import (
	"errors"
	"os"
	"os/exec"
	"os/user"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// unreadable keeps p from being read until the test ends, as mode 0000 does on Unix: an entry in
// its ACL denies this user reading the file's data, or listing the folder, which the folder's
// files and subfolders inherit, so nothing in it can be read either. The attributes stay
// readable, so Stat answers as it does on Unix. A process whose backup privilege is enabled
// reads through any ACL, as root does; an OpenSSH session's is, so it is disabled for the test.
func unreadable(t *testing.T, p string) {
	t.Helper()
	withoutBackupPrivilege(t)
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	sid := "*" + u.Uid
	rights := "(RD)"
	if fi.IsDir() {
		rights = "(OI)(CI)(RD)"
	}
	if out, err := exec.Command("icacls", p, "/deny", sid+":"+rights).CombinedOutput(); err != nil {
		t.Fatalf("icacls /deny: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("icacls", p, "/remove:d", sid).CombinedOutput(); err != nil {
			t.Errorf("icacls /remove:d: %v\n%s", err, out)
		}
	})
}

// withoutBackupPrivilege disables SeBackupPrivilege in this process's token until the test ends,
// when it is put back as it was.
func withoutBackupPrivilege(t *testing.T) {
	t.Helper()
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tok.Close() })
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr("SeBackupPrivilege"), &luid); err != nil {
		t.Fatal(err)
	}
	off := windows.Tokenprivileges{PrivilegeCount: 1, Privileges: [1]windows.LUIDAndAttributes{{Luid: luid}}}
	var before windows.Tokenprivileges
	var n uint32
	err := windows.AdjustTokenPrivileges(tok, false, &off, uint32(unsafe.Sizeof(before)), &before, &n)
	if errors.Is(err, windows.ERROR_NOT_ALL_ASSIGNED) { // not held: nothing to disable
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if before.PrivilegeCount == 0 { // it was disabled already
			return
		}
		if err := windows.AdjustTokenPrivileges(tok, false, &before, 0, nil, nil); err != nil {
			t.Error(err)
		}
	})
}
