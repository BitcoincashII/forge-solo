package main

import (
	"fmt"
	"os"
	"sync"
	"time"
)

var logMu sync.Mutex

// launcherLogLimit is how big launcher.log may grow before it is moved aside to launcher.log.1.
const launcherLogLimit = 1 << 20

// logf adds a line to launcher.log in the data folder: what the launcher started and stopped, and
// when, so that a stop cut short (by Windows ending the session, say) can be told from one that
// finished. The services keep their own logs. Past its limit the log is moved aside, while running
// too: a program that keeps failing to start is logged each time.
func logf(format string, args ...any) {
	if dataDir == "" {
		return
	}
	logMu.Lock()
	defer logMu.Unlock()
	rotateLog(dpath("launcher.log"), launcherLogLimit)
	f, err := os.OpenFile(dpath("launcher.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), fmt.Sprintf(format, args...))
}

// serviceLogLimit is how big a service's log may grow before it is moved aside to <name>.1.
const serviceLogLimit = 20 << 20

// A cappedLog is a service's log in the data folder (stratum.log, api.log): on Windows nothing
// else keeps what the services print (Umbrel has Docker's logs, Linux the journal). Past its limit
// it is moved to <name>.1, replacing the one before. A write never fails: an error would stop the
// copying from the service's output, and a service writing to a full pipe stops with it.
type cappedLog struct {
	mu    sync.Mutex
	path  string
	limit int64
	f     *os.File
	size  int64
	// retryAt is the size at which a move aside that failed is tried again.
	retryAt int64
}

func (l *cappedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil && !l.open() {
		return len(p), nil
	}
	if l.size > 0 && l.size+int64(len(p)) > l.limit && l.size >= l.retryAt {
		_ = l.f.Close()
		l.f = nil
		moved := moveAside(l.path)
		if !l.open() {
			return len(p), nil
		}
		// A viewer can hold the log for hours, and each try closes and reopens it. So a move
		// that failed is tried again once the log has grown by another twentieth of its limit
		// (1 MB for a service's log), not at every line: after the viewer lets go, the log is
		// moved at the next of those tries, hours or days later.
		l.retryAt = 0
		if !moved {
			l.retryAt = l.size + l.limit/20
		}
	}
	n, _ := l.f.Write(p)
	l.size += int64(n)
	return len(p), nil
}

// open opens the log to add to it, and notes how big it is.
func (l *cappedLog) open() bool {
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	l.f, l.size = f, 0
	if st, err := f.Stat(); err == nil {
		l.size = st.Size()
	}
	return true
}

// renameFile and removeFile are the file operations a move aside makes (stand-ins in the tests:
// Linux lets a program rename and delete a file that another program has open).
var (
	renameFile = os.Rename
	removeFile = os.Remove
)

// moveAside moves the log at path to path.1, replacing the one before, and says whether it did.
// Windows refuses to rename or delete a file that a program, a log viewer say, has open without
// sharing delete access. So the log is renamed to path.rotating first: if that fails, nothing has
// changed and the log before is still in .1. Only then is .1 deleted (a rename does not replace a
// file that is read-only or open in a viewer) and replaced. If it cannot be, the log is put back.
func moveAside(path string) bool {
	rotating := path + ".rotating"
	if renameFile(path, rotating) != nil {
		return false
	}
	_ = removeFile(path + ".1")
	if renameFile(rotating, path+".1") == nil {
		return true
	}
	_ = renameFile(rotating, path)
	return false
}

var (
	serviceLogsMu sync.Mutex
	serviceLogs   = map[string]*cappedLog{}
)

// serviceLog is the log for the named service, one per name: a restarted service goes on in the
// same file.
func serviceLog(name string) *cappedLog {
	serviceLogsMu.Lock()
	defer serviceLogsMu.Unlock()
	path := dpath(name + ".log")
	if l := serviceLogs[path]; l != nil {
		return l
	}
	l := &cappedLog{path: path, limit: serviceLogLimit}
	serviceLogs[path] = l
	return l
}
