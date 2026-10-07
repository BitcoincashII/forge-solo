//go:build unix

package pgmigrate

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/BitcoincashII/forge-solo/internal/migstatus"
)

// What EnsureOwner calls, so a test can see it work without being root.
var (
	geteuid = os.Geteuid
	lchown  = os.Lchown
	chmod   = os.Chmod
)

// EnsureOwner gives the database's folder and files to the app's user when the migrator runs as
// root (the Umbrel container): the folder 0700, and forgesolo.db and everything beside it that
// belongs to it (its WAL and in-use lock, the copies being made or kept, the marker, the status and
// the skip file) 0600. Docker creates a new folder as root, and a file the root migrator made would
// otherwise leave the api and the stratum unable to write. It runs at the start and at the end of
// every run. As any other user, or without an owner, it does nothing.
func EnsureOwner(db string, o *Owner) error {
	if o == nil || geteuid() != 0 {
		return nil
	}
	dir := filepath.Dir(db)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := lchown(dir, o.UID, o.GID); err != nil {
		return err
	}
	if err := chmod(dir, 0o700); err != nil {
		return err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if !e.Type().IsRegular() || !belongsToDB(db, e.Name()) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if err := lchown(p, o.UID, o.GID); err != nil {
			return err
		}
		if err := chmod(p, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// belongsToDB reports whether a file named name, beside the database at db, is one of the
// database's own.
func belongsToDB(db, name string) bool {
	for _, p := range []string{filepath.Base(db), MarkerName, migstatus.FileName, SkipName} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
