//go:build unix

package pgmigrate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/migstatus"
)

// As root (the Umbrel container) every file of the database goes to the app's user, 0600, and the
// folder 0700; nothing else in the folder is touched.
func TestCommitGivesFilesToTheApp(t *testing.T) {
	type call struct {
		path string
		uid  int
		mode os.FileMode
	}
	var calls []call
	geteuid = func() int { return 0 }
	lchown = func(p string, uid, gid int) error { calls = append(calls, call{path: p, uid: uid}); return nil }
	chmod = func(p string, m os.FileMode) error {
		calls = append(calls, call{path: p, mode: m})
		return os.Chmod(p, m)
	}
	defer func() { geteuid, lchown, chmod = os.Geteuid, os.Lchown, os.Chmod }()

	dir := t.TempDir()
	db, pgdata := filepath.Join(dir, "forgesolo.db"), oldData(t)
	src := mergeSetup(t, db, pgdata)
	prepareMerge(t, src, db)
	must(t, os.WriteFile(filepath.Join(dir, "unrelated.txt"), nil, 0o644))
	calls = nil
	if _, err := Commit(context.Background(), CommitOptions{DB: db, PGData: pgdata, Owner: &Owner{UID: 10001, GID: 10001}}); err != nil {
		t.Fatal(err)
	}
	chowned, modes := map[string]int{}, map[string]os.FileMode{}
	for _, c := range calls {
		if c.uid != 0 {
			chowned[filepath.Base(c.path)] = c.uid
		} else {
			modes[filepath.Base(c.path)] = c.mode
		}
	}
	for _, f := range dirFiles(t, dir) {
		if f == "unrelated.txt" {
			if _, ok := chowned[f]; ok {
				t.Errorf("MIG-OWNER: %s, not the database's, was given to the app", f)
			}
			continue
		}
		if chowned[f] != 10001 || modes[f] != 0o600 {
			t.Errorf("MIG-OWNER: %s is not given to 10001 with 0600 (uid %d, mode %o)", f, chowned[f], modes[f])
		}
	}
	if chowned[filepath.Base(dir)] != 10001 || modes[filepath.Base(dir)] != 0o700 {
		t.Errorf("MIG-OWNER: the folder is not given to 10001 with 0700")
	}
	for _, want := range []string{"forgesolo.db", "forgesolo.db.inuse", MarkerName, migstatus.FileName} {
		if chowned[want] != 10001 {
			t.Errorf("MIG-OWNER: %s was not given to the app", want)
		}
	}
}

// A commit as an ordinary user, or with no owner, changes no owner.
func TestEnsureOwnerOnlyAsRoot(t *testing.T) {
	called := false
	lchown = func(string, int, int) error { called = true; return nil }
	defer func() { lchown = os.Lchown }()
	db := filepath.Join(t.TempDir(), "forgesolo.db")
	must(t, os.WriteFile(db, nil, 0o600))
	geteuid = func() int { return 1000 }
	must(t, EnsureOwner(db, &Owner{UID: 10001, GID: 10001}))
	geteuid = func() int { return 0 }
	must(t, EnsureOwner(db, nil))
	geteuid = os.Geteuid
	if called {
		t.Fatal("MIG-OWNER-NOTROOT: owners were changed without root or without an owner")
	}
}
