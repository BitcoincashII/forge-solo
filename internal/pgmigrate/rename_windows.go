//go:build windows

package pgmigrate

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// renameRetryFor is how long a rename goes on trying while Windows refuses it because a file is
// open: an antivirus scan of the new file, or an indexer, holds it for moments.
const renameRetryFor = 5 * time.Second

// renameReplacing renames from over to, which may exist, trying again while Windows answers
// ACCESS_DENIED or SHARING_VIOLATION.
func renameReplacing(from, to string) error {
	deadline := time.Now().Add(renameRetryFor)
	for {
		err := os.Rename(from, to)
		if err == nil || time.Now().After(deadline) ||
			!(errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}
