package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

// pgPath is path in a form the bundled PostgreSQL programs can take: path itself when they cannot
// take it in any form (pgForm says why).
func pgPath(path string) string {
	form, _ := pgForm(path)
	return form
}

// pgForm is path in a form the bundled PostgreSQL programs can take. They read their arguments, and
// find their own folder, in the system's ANSI code page, so a character outside it (a user name in
// another script, which C:\Users\<name> carries into every path here) reached them as "?", and the
// database never started ("program postgres is needed by initdb but was not found").
//
// Each part of the path with a character beyond ASCII is given by the short (8.3) name Windows
// keeps for it, which is ASCII, or else as it is when the code page holds it. A part that is
// neither is named in bad, with path itself as form. Short names are read part by part, as the
// drive stores them: GetShortPathName leaves a part long when the code page's look-alike of it
// would be a valid short name ("Pawel" for a name with a Polish l), though the drive keeps one.
func pgForm(path string) (form, bad string) {
	if isASCII(path) {
		return path, ""
	}
	vol := filepath.VolumeName(path)
	if !isASCII(vol) && !codePageHolds(vol) {
		return path, vol
	}
	out := []byte(vol)
	start := len(vol)
	for i := start; i <= len(path); i++ {
		if i < len(path) && !os.IsPathSeparator(path[i]) {
			continue
		}
		name := path[start:i]
		if !isASCII(name) {
			if s, err := shortName(path[:i]); err == nil && s != "" && isASCII(s) {
				name = s
			} else if !codePageHolds(name) {
				return path, name
			}
		}
		out = append(out, name...)
		if i < len(path) {
			out = append(out, path[i])
		}
		start = i + 1
	}
	return string(out), ""
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
