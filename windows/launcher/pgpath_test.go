package main

import (
	"errors"
	"path/filepath"
	"testing"
)

// codePage stands in, for the test, for the short (8.3) names a Windows drive keeps (by the path
// of the file or folder) and for the names the system's code page holds.
func codePage(t *testing.T, short map[string]string, holds ...string) {
	t.Helper()
	savedShort, savedHolds := shortName, codePageHolds
	shortName = func(p string) (string, error) {
		if s, ok := short[p]; ok {
			return s, nil
		}
		return "", errors.New("no such file")
	}
	codePageHolds = func(s string) bool {
		for _, h := range holds {
			if s == h {
				return true
			}
		}
		return false
	}
	t.Cleanup(func() { shortName, codePageHolds = savedShort, savedHolds })
}

var (
	cn    = string([]rune{0x6D4B, 0x8BD5}) // a Chinese name, which a Western code page lacks
	pawel = "Pawe\u0142"                   // a Polish l, which a Western code page lacks
	jose  = "Jos\u00e9"                    // an e acute, which a Western code page holds
)

// pgPath is pgForm's form alone.
func pgPath(path string) string {
	form, _ := pgForm(path)
	return form
}

// A path in plain ASCII is given as it is: nothing changes for the users this was never a problem
// for.
func TestPgPathASCII(t *testing.T) {
	codePage(t, map[string]string{"/Users/dev/AppData/Roaming/ForgeSolo": "FORGES~1"})
	if got := pgPath("/Users/dev/AppData/Roaming/ForgeSolo/pgdata"); got != "/Users/dev/AppData/Roaming/ForgeSolo/pgdata" {
		t.Errorf("PGPATH-ASCII: an ASCII path was changed to %q", got)
	}
}

// A part with a character outside the code page is given by the short name the drive keeps for
// it, which is ASCII; the other parts stay as they are. A file not made yet keeps its own name.
func TestPgPathUsesTheShortNamesTheDriveKeeps(t *testing.T) {
	codePage(t, map[string]string{
		"/Users/" + cn: "5B3D~1",
		"/Users/" + cn + "/AppData/Roaming/ForgeSolo/" + cn: "6570~1",
	})
	if got := pgPath("/Users/" + cn + "/AppData/Roaming/ForgeSolo/pgdata"); got != "/Users/5B3D~1/AppData/Roaming/ForgeSolo/pgdata" {
		t.Errorf("PGPATH-SHORT: given as %q, not by its short name", got)
	}
	if got := pgPath("/Users/" + cn + "/AppData/Roaming/ForgeSolo/" + cn); got != "/Users/5B3D~1/AppData/Roaming/ForgeSolo/6570~1" {
		t.Errorf("PGPATH-SHORT-LAST: a path whose last part is outside the code page was given as %q", got)
	}
	if got := pgPath("/Users/" + cn + "/AppData/Roaming/ForgeSolo/pglog.txt"); got != "/Users/5B3D~1/AppData/Roaming/ForgeSolo/pglog.txt" {
		t.Errorf("PGPATH-NEW-FILE: a file not made yet was given as %q", got)
	}
}

// A user name like "Pawel" with a Polish l: the drive keeps a short name for it (PAWE~1), but
// GetShortPathName leaves the part long, since the code page's look-alike "Pawel" would be a valid
// short name. Read part by part, the short name is found.
func TestPgPathFindsTheShortNameGetShortPathNameMisses(t *testing.T) {
	codePage(t, map[string]string{"C:/Users/" + pawel: "PAWE~1"})
	if got, bad := pgForm("C:/Users/" + pawel + "/AppData/Roaming/ForgeSolo/pgdata"); got != "C:/Users/PAWE~1/AppData/Roaming/ForgeSolo/pgdata" || bad != "" {
		t.Errorf("PGPATH-STORED-SHORT: given as %q (cannot take %q), not by the short name the drive keeps", got, bad)
	}
}

// A drive with short names off (often one other than C:): a name the code page holds is given as
// it is, as the release before did, and PostgreSQL takes it.
func TestPgPathKeepsANameTheCodePageHolds(t *testing.T) {
	codePage(t, nil, jose)
	if got, bad := pgForm("D:/Users/" + jose + "/AppData/Roaming/ForgeSolo/pgdata"); got != "D:/Users/"+jose+"/AppData/Roaming/ForgeSolo/pgdata" || bad != "" {
		t.Errorf("PGPATH-CODE-PAGE: given as %q (cannot take %q); the code page holds %q", got, bad, jose)
	}
}

// A name neither shortened nor held by the code page: there is no form PostgreSQL can take, and
// pgForm names the part at fault.
func TestPgPathNamesThePartItCannotGive(t *testing.T) {
	codePage(t, nil, jose)
	path := "D:/Users/" + pawel + "/AppData/Roaming/ForgeSolo/pgdata"
	if got, bad := pgForm(path); got != path || bad != pawel {
		t.Errorf("PGPATH-NEITHER: gave %q, naming %q as the part it cannot take; want the path as it is and %q", got, bad, pawel)
	}
}

// The programs themselves start from, and in, paths they can take: they find their own folder in
// the code page too ("program postgres is needed by initdb but was not found").
func TestPostgresProgramsStartFromShortPaths(t *testing.T) {
	saved, savedData := installDir, dataDir
	installDir, dataDir = filepath.FromSlash("/Users/"+cn+"/Programs/ForgeSolo"), t.TempDir()
	t.Cleanup(func() { installDir, dataDir = saved, savedData })
	codePage(t, map[string]string{filepath.FromSlash("/Users/" + cn): "5B3D~1"})
	r, err := reachPostgres()
	if err != nil {
		t.Fatal(err)
	}
	c := pgCmd(r, "pg_ctl.exe", "start")
	dir := filepath.FromSlash("/Users/5B3D~1/Programs/ForgeSolo")
	if c.Path != filepath.Join(dir, "pgsql", "bin", "pg_ctl.exe") || c.Args[0] != c.Path || c.Dir != dir {
		t.Fatalf("PGPATH-CMD: pg_ctl runs as %q (args[0] %q) in %q", c.Path, c.Args[0], c.Dir)
	}
}
