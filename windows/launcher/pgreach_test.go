package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// westernCodePage stands in for a Western Windows (code page 1252), which holds an e acute or a u
// umlaut but no Chinese and no Polish l, and for the short (8.3) names its drive keeps, by path.
// Outside Windows a symbolic link stands in for a junction: os.Remove removes either alone.
func westernCodePage(t *testing.T, short map[string]string) {
	t.Helper()
	savedShort, savedHolds, savedJunction, savedKey := shortName, codePageHolds, makeJunction, accountKey
	shortName = func(p string) (string, error) {
		if s, ok := short[p]; ok {
			return s, nil
		}
		return "", errors.New("no short name")
	}
	codePageHolds = func(s string) bool { return !strings.ContainsAny(s, cn+"ł") }
	if runtime.GOOS != "windows" {
		makeJunction = func(link, target string) error { return os.Symlink(target, link) }
	}
	accountKey = func() (string, error) { return "0123abcd", nil }
	t.Cleanup(func() {
		shortName, codePageHolds, makeJunction, accountKey = savedShort, savedHolds, savedJunction, savedKey
	})
}

// folder makes a folder under the test's own, with a file in it, and returns its path.
func folder(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(append([]string{t.TempDir()}, parts...)...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "PG_VERSION"), []byte("16\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The bundled PostgreSQL is given every folder of a move in a form it can take, the plainest
// first: a path the code page holds as it is, as 1.0.12 gave it (a user named Jürgen, José or
// François); else the short names the drive keeps; else a junction under %ProgramData%, for a name
// in another script on a drive that keeps no short names. Each form leads to the folder itself.
func TestPgReachable(t *testing.T) {
	jurgen := "Jürgen"
	pd := folder(t, "ProgramData")
	t.Setenv("ProgramData", pd)

	held := folder(t, "Users", jurgen, "AppData", "Roaming", "ForgeSolo")
	westernCodePage(t, map[string]string{filepath.Dir(filepath.Dir(filepath.Dir(held))): "JRGEN~1"})
	if form, link, err := pgReachable(held, "data"); err != nil || form != held || link != "" {
		t.Errorf("PGREACH-ACP: a path the code page holds is given as %q (junction %q, %v), want it as it is", form, link, err)
	}

	user := folder(t, "Users", cn)
	shortened := filepath.Join(user, "AppData")
	md(shortened)
	westernCodePage(t, map[string]string{user: "5B3D~1"})
	want := filepath.Join(filepath.Dir(user), "5B3D~1", "AppData")
	if form, link, err := pgReachable(shortened, "data"); err != nil || form != want || link != "" {
		t.Errorf("PGREACH-SHORT: a name the code page lacks is given as %q (junction %q, %v), want its short name %q", form, link, err, want)
	}

	westernCodePage(t, nil)
	form, link, err := pgReachable(user, "data")
	wantLink := filepath.Join(pd, "ForgeSolo", "links", "0123abcd", "data")
	if err != nil || form != wantLink || link != wantLink || !isASCII(form) {
		t.Fatalf("PGREACH-JUNCTION: with no short name the folder is given as %q (junction %q, %v), want the junction %q", form, link, err, wantLink)
	}
	if b, err := os.ReadFile(filepath.Join(form, "PG_VERSION")); err != nil || string(b) != "16\n" {
		t.Fatalf("PGREACH-JUNCTION-LEADS: the junction does not lead to the folder: %q %v", b, err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}

	// A junction that leads elsewhere (something put there meanwhile) is not used.
	elsewhere, real := folder(t, "elsewhere"), makeJunction
	makeJunction = func(link, _ string) error { return real(link, elsewhere) }
	if form, _, err := pgReachable(user, "data"); err == nil {
		t.Errorf("PGREACH-JUNCTION-CHECKED: a junction to another folder was used: %q", form)
	}
	if _, err := os.Lstat(wantLink); !os.IsNotExist(err) {
		t.Errorf("PGREACH-JUNCTION-CHECKED: the wrong junction was left (%v)", err)
	}
	westernCodePage(t, nil)

	t.Setenv("ProgramData", folder(t, "Program"+cn))
	if form, _, err := pgReachable(user, "data"); err == nil {
		t.Errorf("PGREACH-PROGRAMDATA: with %%ProgramData%% outside the code page the folder was given as %q", form)
	}
}

// A move's junctions are removed once it is over, and those a move cut short left are removed at
// the next start. Only the junctions go: what they lead to, the old data, is all there.
func TestJunctionsAreRemovedAndTheirFoldersKept(t *testing.T) {
	saved, savedInst := dataDir, installDir
	t.Cleanup(func() { dataDir, installDir = saved, savedInst })
	pd := folder(t, "ProgramData")
	t.Setenv("ProgramData", pd)
	westernCodePage(t, nil)
	dataDir = folder(t, "Users", cn, "AppData", "Roaming", "ForgeSolo")
	installDir = folder(t, "Users", cn, "AppData", "Local", "Programs", "ForgeSolo")
	links := filepath.Join(pd, "ForgeSolo", "links", "0123abcd")

	r, err := reachPostgres()
	if err != nil || r.data != filepath.Join(links, "data") || r.app != filepath.Join(links, "app") {
		t.Fatalf("JUNCTION-BOTH: %+v %v", r, err)
	}
	r.remove()
	for _, p := range []string{filepath.Join(links, "data"), filepath.Join(links, "app"), links} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("JUNCTION-REMOVED: %s is still there after the move (%v)", p, err)
		}
	}

	if _, err := reachPostgres(); err != nil { // a move cut short: its junctions are left
		t.Fatal(err)
	}
	removeLeftLinks()
	if _, err := os.Lstat(links); !os.IsNotExist(err) {
		t.Errorf("JUNCTION-LEFT-REMOVED: the junctions a move cut short left are still there (%v)", err)
	}
	for _, f := range []string{filepath.Join(dataDir, "PG_VERSION"), filepath.Join(installDir, "PG_VERSION")} {
		if b, err := os.ReadFile(f); err != nil || string(b) != "16\n" {
			t.Fatalf("JUNCTION-TARGET-KEPT: removing the junctions took %s with them (%v)", f, err)
		}
	}
}
