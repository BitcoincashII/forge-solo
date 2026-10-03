package main

import (
	"errors"
	"path/filepath"
	"testing"
)

// shortNames stands in for the short (8.3) names a Windows drive keeps, for the test.
func shortNames(t *testing.T, names map[string]string) {
	t.Helper()
	saved := shortPath
	shortPath = func(p string) (string, error) {
		if s, ok := names[p]; ok {
			return s, nil
		}
		return "", errors.New("no such file")
	}
	t.Cleanup(func() { shortPath = saved })
}

// The bundled PostgreSQL takes its paths in the system's code page: one with a character outside it
// is given in its short form, which is ASCII. Plain ASCII is given as it is.
func TestPostgresGetsPathsItCanTake(t *testing.T) {
	cn := "/Users/" + string([]rune{0x6D4B, 0x8BD5}) + "/AppData/Roaming/ForgeSolo"
	shortNames(t, map[string]string{
		cn:             "/Users/5B3D~1/AppData/Roaming/FORGES~1",
		cn + "/pgdata": "/Users/5B3D~1/AppData/Roaming/FORGES~1/pgdata",
		cn + "/" + string([]rune{0x6570, 0x636E}): "/Users/5B3D~1/AppData/Roaming/FORGES~1/6570~1",
		"/Users/José/x": "/Users/José/x", // the drive keeps no short names
		// An ASCII path has a short name too, which must not be used: nothing changes for the users
		// this was never a problem for.
		"/Users/dev/AppData/Roaming/ForgeSolo/pgdata": "/Users/dev/AppData/Roaming/FORGES~1/pgdata",
	})
	if got := pgPath("/Users/dev/AppData/Roaming/ForgeSolo/pgdata"); got != "/Users/dev/AppData/Roaming/ForgeSolo/pgdata" {
		t.Errorf("PGPATH-ASCII: an ASCII path was changed to %q", got)
	}
	if got := pgPath(cn + "/pgdata"); got != "/Users/5B3D~1/AppData/Roaming/FORGES~1/pgdata" {
		t.Errorf("PGPATH-SHORT: %q was given as %q, not in its short form", cn+"/pgdata", got)
	}
	// Its own name outside the code page too: only its own short form will do.
	if got := pgPath(cn + "/" + string([]rune{0x6570, 0x636E})); got != "/Users/5B3D~1/AppData/Roaming/FORGES~1/6570~1" {
		t.Errorf("PGPATH-SHORT: a path whose last part is outside the code page was given as %q", got)
	}
	if got := pgPath(cn + "/pglog.txt"); got != filepath.Join("/Users/5B3D~1/AppData/Roaming/FORGES~1", "pglog.txt") {
		t.Errorf("PGPATH-NEW-FILE: a file not made yet was given as %q, not in its folder's short form", got)
	}
	if got := pgPath("/Users/José/x"); got != "/Users/José/x" {
		t.Errorf("PGPATH-NO-SHORT: with no short name, the path was given as %q", got)
	}
}

// The programs themselves start from, and in, their short paths: they find their own folder in the
// code page too ("program postgres is needed by initdb but was not found").
func TestPostgresProgramsStartFromShortPaths(t *testing.T) {
	saved := installDir
	installDir = "/Users/" + string([]rune{0x6D4B, 0x8BD5}) + "/Programs/ForgeSolo"
	t.Cleanup(func() { installDir = saved })
	shortNames(t, map[string]string{
		installDir:                             "/Users/5B3D~1/Programs/FORGES~1",
		installDir + "/pgsql\\bin\\pg_ctl.exe": "/Users/5B3D~1/Programs/FORGES~1/pgsql/bin/pg_ctl.exe",
	})
	c := pgCmd("pgsql\\bin\\pg_ctl.exe", "start")
	if c.Path != "/Users/5B3D~1/Programs/FORGES~1/pgsql/bin/pg_ctl.exe" || c.Args[0] != c.Path || c.Dir != "/Users/5B3D~1/Programs/FORGES~1" {
		t.Fatalf("PGPATH-CMD: pg_ctl runs as %q (args[0] %q) in %q", c.Path, c.Args[0], c.Dir)
	}
}
