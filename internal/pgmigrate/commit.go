package pgmigrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/dblock"
	"github.com/BitcoincashII/forge-solo/internal/migstatus"
)

// Owner is the user and group the app's programs run as, who must own the database's files.
type Owner struct{ UID, GID int }

// ParseOwner reads "uid:gid".
func ParseOwner(s string) (*Owner, error) {
	u, g, ok := strings.Cut(s, ":")
	uid, err1 := strconv.Atoi(u)
	gid, err2 := strconv.Atoi(g)
	if !ok || err1 != nil || err2 != nil || uid < 0 || gid < 0 {
		return nil, fmt.Errorf("owner %q: want uid:gid", s)
	}
	return &Owner{UID: uid, GID: gid}, nil
}

// beforeMergePrefix starts the name of the copy of forgesolo.db a merge keeps of what it replaced.
const beforeMergePrefix = ".before-merge-"

// now is the time; a test sets it.
var now = time.Now

// CommitOptions says what to commit.
type CommitOptions struct {
	DB      string // forgesolo.db
	PGData  string // the old data
	Owner   *Owner // when set and running as root, the owner of every file (EnsureOwner)
	Version string
	Logf    func(format string, args ...any)
	// InUse is the database's in-use lock when the caller (run) already holds it exclusively.
	InUse *dblock.Lock
}

// Committed is what a commit did.
type Committed struct {
	Mode        string
	Marker      Marker
	BeforeMerge string // the copy of what a merge replaced
}

// Commit puts the prepared copy in the database's place, with PostgreSQL stopped:
//  1. Nobody may have the database open: the in-use lock and SQLite's own exclusive lock, both
//     without waiting. Busy is deferred (31), with nothing changed.
//  2. Refused (30) while PostgreSQL runs on the old data (postmaster.pid), when pg_control does not
//     say it was shut down cleanly, when no copy is prepared, when forgesolo.db appeared since a
//     move was prepared, or changed since a merge was.
//  3. A merge first copies forgesolo.db to forgesolo.db.before-merge-<UTC>.
//  4. The database's -wal, -shm and -journal are deleted: a WAL left beside the new file would
//     turn it back into the old database.
//  5. The copy is renamed into place, and the folder synced.
//  6. postgres-migrated.json records pg_control's hash as it is now, and the status file says done.
//  7. A merge keeps only the newest before-merge copy.
//
// A crash before the rename leaves the old state, and one after it a database without a marker,
// which the next start merges again to the same result.
func Commit(ctx context.Context, o CommitOptions) (*Committed, error) {
	if o.InUse == nil {
		l, err := dblock.TryExclusive(dblock.Path(o.DB))
		if errors.Is(err, dblock.ErrBusy) {
			return nil, deferred(fmt.Errorf("a program holds %s", dblock.Path(o.DB)))
		}
		if err != nil {
			return nil, newErr(CodeOther, "the database's in-use lock could not be taken", err)
		}
		defer l.Release()
	}
	dbThere, err := exists(o.DB)
	if err != nil {
		return nil, newErr(CodeOther, "the database folder could not be read", err)
	}
	if dbThere {
		db, conn, err := exclusive(ctx, o.DB)
		if err != nil {
			return nil, err
		}
		conn.Close()
		db.Close()
	}

	if pid, err := exists(filepath.Join(o.PGData, "postmaster.pid")); err != nil || pid {
		return nil, refused("PostgreSQL is still running on the old data", err)
	}
	ctl, err := ReadControl(o.PGData)
	if err != nil {
		return nil, refused("the old data's pg_control cannot be read", err)
	}
	if ctl.State != StateShutDown {
		return nil, refused("the old data was not shut down cleanly", fmt.Errorf("pg_control state %d", ctl.State))
	}
	ver, err := os.ReadFile(filepath.Join(o.PGData, "PG_VERSION"))
	if err != nil || strings.TrimSpace(string(ver)) != "16" {
		return nil, refused("the old data is not PostgreSQL 16", err)
	}

	tmp := MigratingPath(o.DB)
	meta, err := ReadMeta(ctx, tmp)
	if err != nil {
		return nil, refused("there is no prepared copy to put in place", err)
	}
	if meta["state"] != "prepared" {
		return nil, refused("the copy was not prepared to the end", fmt.Errorf("state %q", meta["state"]))
	}
	if fi, err := os.Stat(tmp + "-wal"); err == nil && fi.Size() > 0 {
		return nil, refused("the prepared copy was changed after it was prepared", fmt.Errorf("%d bytes in its WAL", fi.Size()))
	}
	mode := meta["mode"]
	switch mode {
	case ModeMove:
		if dbThere {
			return nil, refused("forgesolo.db appeared after the copy was prepared", nil)
		}
	case ModeMerge:
		var want identity
		if err := json.Unmarshal([]byte(meta["existing"]), &want); err != nil {
			return nil, refused("the prepared merge does not record forgesolo.db", err)
		}
		got, err := fileIdentity(o.DB)
		if err != nil || got != want {
			return nil, refused("forgesolo.db changed after the copy was prepared", err)
		}
	default:
		return nil, refused("the prepared copy has no mode", fmt.Errorf("mode %q", mode))
	}

	c := &Committed{Mode: mode}
	if mode == ModeMerge {
		c.BeforeMerge = o.DB + beforeMergePrefix + now().UTC().Format("20060102T150405Z")
		if err := copyDurably(o.DB, c.BeforeMerge); err != nil {
			return nil, newErr(CodeWrite, "forgesolo.db could not be copied aside", err)
		}
	}
	for _, f := range []string{o.DB + "-wal", o.DB + "-shm", o.DB + "-journal", tmp + "-wal", tmp + "-shm", tmp + "-journal", tmp + ".inuse"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, newErr(CodeWrite, "an old WAL file could not be removed", err)
		}
	}
	if err := stage("renaming"); err != nil {
		return nil, err
	}
	if err := renameReplacing(tmp, o.DB); err != nil {
		return nil, newErr(CodeWrite, "the new database could not be put in place", err)
	}
	if err := migstatus.SyncDir(filepath.Dir(o.DB)); err != nil {
		return nil, newErr(CodeWrite, "the new database could not be synced", err)
	}
	if err := stage("renamed"); err != nil {
		return nil, err
	}

	c.Marker = Marker{PGControlSHA256: ctl.SHA256, PGVersion: "16", Mode: mode,
		MigratedAt: now().UTC().Format(time.RFC3339), Version: o.Version}
	_ = json.Unmarshal([]byte(meta["counts"]), &c.Marker.Counts) // only for the record
	b, _ := json.MarshalIndent(c.Marker, "", "  ")
	if err := migstatus.WriteDurably(MarkerPath(o.DB), append(b, '\n')); err != nil {
		return nil, newErr(CodeWrite, "postgres-migrated.json could not be written", err)
	}
	if err := migstatus.Write(o.DB, migstatus.Status{State: migstatus.Done, Version: o.Version,
		Reason: "the data of the earlier version is in forgesolo.db"}); err != nil {
		return nil, newErr(CodeWrite, "the status file could not be written", err)
	}
	if mode == ModeMerge {
		if err := keepNewestBeforeMerge(o.DB, c.BeforeMerge); err != nil && o.Logf != nil {
			o.Logf("an older copy of forgesolo.db could not be removed: %v", err)
		}
	}
	if err := EnsureOwner(o.DB, o.Owner); err != nil {
		return nil, newErr(CodeWrite, "the database's files could not be given to the app's user", err)
	}
	return c, nil
}

// keepNewestBeforeMerge deletes every before-merge copy of db except keep.
func keepNewestBeforeMerge(db, keep string) error {
	ents, err := os.ReadDir(filepath.Dir(db))
	if err != nil {
		return err
	}
	prefix := filepath.Base(db) + beforeMergePrefix
	var errs []error
	for _, e := range ents {
		p := filepath.Join(filepath.Dir(db), e.Name())
		if strings.HasPrefix(e.Name(), prefix) && p != keep && !e.IsDir() {
			errs = append(errs, os.Remove(p))
		}
	}
	return errors.Join(errs...)
}

// copyDurably copies src to dst through a file beside dst that is synced and then renamed.
func copyDurably(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return migstatus.SyncDir(filepath.Dir(dst))
}
