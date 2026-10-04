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
}

func (l *cappedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return len(p), nil
		}
		l.f, l.size = f, 0
		if st, err := f.Stat(); err == nil {
			l.size = st.Size()
		}
	}
	if l.size > 0 && l.size+int64(len(p)) > l.limit {
		_ = l.f.Close()
		l.f = nil
		_ = os.Remove(l.path + ".1")
		_ = os.Rename(l.path, l.path+".1")
		f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return len(p), nil
		}
		l.f, l.size = f, 0
	}
	n, _ := l.f.Write(p)
	l.size += int64(n)
	return len(p), nil
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
