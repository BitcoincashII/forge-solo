package main

import (
	"fmt"
	"os"
	"sync"
	"time"
)

var logMu sync.Mutex

// logf adds a line to launcher.log in the data folder: what the launcher started and stopped, and
// when, so that a stop cut short (by Windows ending the session, say) can be told from one that
// finished. The services keep their own logs.
func logf(format string, args ...any) {
	if dataDir == "" {
		return
	}
	logMu.Lock()
	defer logMu.Unlock()
	f, err := os.OpenFile(dpath("launcher.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), fmt.Sprintf(format, args...))
}
