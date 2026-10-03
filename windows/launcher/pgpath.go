package main

import (
	"os/exec"
	"path/filepath"
)

// pgPath is path in a form the bundled PostgreSQL programs can take. They read their arguments, and
// find their own folder, in the system's ANSI code page, so a character outside it -- a user name
// in another script, which C:\Users\<name> carries into every path here -- reached them as "?", and
// the database never started ("program postgres is needed by initdb but was not found"). Such a
// path is given in its short (8.3) form, which is ASCII; a file not made yet, as its folder's short
// form and its own name. A path in plain ASCII is given as it is.
func pgPath(path string) string {
	if isASCII(path) {
		return path
	}
	if s, err := shortPath(path); err == nil && isASCII(s) {
		return s
	}
	dir, base := filepath.Split(path)
	if isASCII(base) && dir != "" {
		if s, err := shortPath(filepath.Clean(dir)); err == nil && isASCII(s) {
			return filepath.Join(s, base)
		}
	}
	return path
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// pgCmd runs one of the bundled PostgreSQL programs (name under the install folder), hidden and
// below normal priority, from and with paths it can take (pgPath).
func pgCmd(name string, args ...string) *exec.Cmd {
	c := hiddenPrio(belowNormal, name, args...)
	c.Path = pgPath(ipath(name))
	c.Args[0] = c.Path
	c.Dir = pgPath(installDir)
	return c
}
