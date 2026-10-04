//go:build sqlite && !windows

package pgmigrate

import "os"

// renameReplacing renames from over to, which may exist.
func renameReplacing(from, to string) error { return os.Rename(from, to) }
