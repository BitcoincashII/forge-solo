package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Forge Solo keeps its data in forgesolo.db, one SQLite file the API and the miner share, as on
// Umbrel and Linux. Up to 1.0.12 it kept it in PostgreSQL (pgdata in the data folder). That data is
// moved into forgesolo.db once: forge-solo-migrate.exe copies it from the bundled PostgreSQL,
// started read-only for the move alone, checks the copy, and only then puts it in place. The old
// data stays where it is, for going back to 1.0.12; coming forward again merges what each version
// recorded. After a move, a start runs neither PostgreSQL nor the migrator: the launcher tells from
// the files alone that there is nothing to do (quickPlan).

const (
	migrateExe = "forge-solo-migrate.exe"
	markerName = "postgres-migrated.json"  // a finished move, and the old data's pg_control hash then
	skipName   = "SKIP-POSTGRES-MIGRATION" // start without the old data (the dashboard's button)
)

// What a start does about the old data, as forge-solo-migrate plan says it.
const (
	planNone     = "none"
	planMove     = "move"
	planMerge    = "merge"
	planSkipped  = "skipped"
	planDegraded = "degraded"
	planFailed   = "failed"
)

// The migrator's exit codes (internal/pgmigrate), which the status file carries.
const (
	codeInterrupted = 3
	codeSource      = 10
	codeRefused     = 30
	codeDeferred    = 31
	codeOther       = 40
)

// migrateLimit is how long each of the migrator's steps may take (shorter in the tests).
var migrateLimit = 10 * time.Minute

// pgPortFrom is the first port of the window the old database's port is picked from, during a move
// (a variable so that the tests can use another).
var pgPortFrom = 30000

func dbPath() string { return dpath("forgesolo.db") }

// prepareDatabase decides, before anything opens forgesolo.db, what this start does about an
// earlier version's PostgreSQL data, by the table both platforms follow (internal/pgmigrate,
// Plan), and moves or merges it when that is needed. Whatever happens, the start goes on, as on
// Umbrel: a move that fails replaces nothing, the API serves the dashboard's maintenance page,
// which says why and offers to start without the old data, and the miner does not mine.
func prepareDatabase() {
	clearTrouble("database")
	setRunningNote("")
	removeLeftLinks()
	action, reason, verified := quickPlan(dbPath(), dpath("pgdata"))
	if action == "" {
		var err error
		action, reason, err = migratorPlan()
		if err != nil {
			if !isStopping() {
				moveFailed(migrationStatus{Code: codeOther, Reason: migrateExe + " could not run", Detail: err.Error()})
			}
			return
		}
	}
	if reason != "" {
		logf("old data: %s (%s)", action, reason)
	} else {
		logf("old data: %s", action)
	}
	switch action {
	case planNone:
		// What an earlier start recorded (a failed move, say) no longer holds, readable or not. A
		// fresh install gets no status file, as on Linux.
		if there, _ := fileThere(statusPath()); there {
			recordStatus(migrationStatus{State: stateNone, Reason: "there is nothing to move"})
		}
		if verified {
			removeMoveTools()
		}
	case planSkipped:
		recordStatus(migrationStatus{State: stateSkipped,
			Reason: skipName + " is there: Forge Solo started without the data of the earlier version"})
	case planDegraded:
		recordStatus(migrationStatus{State: stateDegraded, Reason: reason})
		setRunningNote(noteDegraded)
	case planMove, planMerge:
		moveData(action == planMerge)
	default:
		moveFailed(migrationStatus{Code: codeRefused, Reason: reason})
	}
}

// quickPlan is the start's decision where the files alone tell, as forge-solo-migrate plan would
// make it: internal/pgmigrate/testdata/plan-cases.json holds the cases both must decide alike. It
// is "" where only the migrator can tell: a move, a merge, or old data it refuses. verified is set
// when postgres-migrated.json records a move and the old data has not changed since: a start then
// runs neither PostgreSQL nor the migrator, which an antivirus may hold or remove.
func quickPlan(db, pgdata string) (action, reason string, verified bool) {
	if _, err := os.Lstat(filepath.Join(filepath.Dir(db), skipName)); err == nil {
		return planSkipped, "", false
	}
	ver, verErr := os.ReadFile(filepath.Join(pgdata, "PG_VERSION"))
	if errors.Is(verErr, fs.ErrNotExist) {
		return planNone, "", false
	}
	marker := filepath.Join(filepath.Dir(db), markerName)
	dbThere, dbErr := fileThere(db)
	markerThere, markerErr := fileThere(marker)
	switch {
	case (dbErr != nil || markerErr != nil) && (markerThere || markerErr != nil):
		return planDegraded, "the database folder could not be read", false
	case dbErr != nil || !dbThere || !markerThere:
		return "", "", false
	}
	// Moved before: from here on the old folder can only be ignored, however damaged.
	switch {
	case verErr != nil:
		return planDegraded, "the old database folder cannot be read", false
	case strings.TrimSpace(string(ver)) != "16":
		return planDegraded, "the old database folder's PG_VERSION is damaged", false
	}
	sum, err := controlHash(pgdata)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return planDegraded, "the old database's pg_control is missing", false
	case errors.Is(err, errControlDamaged):
		return planDegraded, "the old database's pg_control is damaged", false
	case err != nil:
		return planDegraded, "the old database's pg_control cannot be read", false
	}
	b, err := os.ReadFile(marker)
	if err != nil {
		return planDegraded, markerName + " cannot be read", false
	}
	var m struct {
		PGControlSHA256 string `json:"pg_control_sha256"`
	}
	if json.Unmarshal(b, &m) == nil && m.PGControlSHA256 == sum {
		return planNone, "", true
	}
	return "", "", false // 1.0.12 ran on the old data since: a merge
}

// fileThere reports whether path is there; an error other than its absence is returned.
func fileThere(path string) (bool, error) {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return false, err
}

var errControlDamaged = errors.New("not a PostgreSQL 16 pg_control whose checksum holds")

// controlHash is the SHA-256 of pgdata's global/pg_control, which PostgreSQL rewrites at every
// start and stop: the hash postgres-migrated.json records tells whether 1.0.12 has run on the old
// data since the move. The file must be PostgreSQL 16's (pg_control_version 1300) with its CRC-32C
// holding, as forge-solo-migrate reads it.
func controlHash(pgdata string) (string, error) {
	b, err := os.ReadFile(filepath.Join(pgdata, "global", "pg_control"))
	if err != nil {
		return "", err
	}
	const versionAt, crcAt, version16 = 8, 288, 1300
	le := binary.LittleEndian
	if len(b) < crcAt+4 || le.Uint32(b[versionAt:]) != version16 ||
		crc32.Checksum(b[:crcAt], crc32.MakeTable(crc32.Castagnoli)) != le.Uint32(b[crcAt:]) {
		return "", errControlDamaged
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// migratorPlan asks forge-solo-migrate plan what this start does.
func migratorPlan() (action, reason string, err error) {
	var out, errOut bytes.Buffer
	c := hidden(migrateExe, "plan", "--db", dbPath(), "--pgdata", dpath("pgdata"))
	c.Stdout, c.Stderr = &out, &errOut
	if err := runToEnd(c, nil); err != nil {
		if s := strings.TrimSpace(errOut.String()); s != "" {
			err = fmt.Errorf("%w: %s", err, s)
		}
		return "", "", err
	}
	line, _, _ := strings.Cut(strings.TrimSpace(out.String()), "\n")
	action, reason, _ = strings.Cut(line, ":")
	switch action {
	case planNone, planMove, planMerge, planSkipped, planDegraded, planFailed:
		return action, reason, nil
	}
	return "", "", fmt.Errorf("its plan was %q", line)
}

// moveData moves the old data into forgesolo.db, or merges it with what forgesolo.db holds:
//  1. the bundled PostgreSQL starts on the old data, read-only and on this PC only, on a port of its
//     own, from paths it can take (pgReachable);
//  2. forge-solo-migrate prepare copies the old data beside forgesolo.db and checks the copy;
//  3. PostgreSQL stops, cleanly;
//  4. forge-solo-migrate commit puts the copy in place and writes postgres-migrated.json.
//
// Quit or Windows ending the session meanwhile ends the migrator and stops PostgreSQL, and nothing
// is put in place: the next start moves the data. A move that fails leaves everything as it was.
func moveData(merge bool) {
	status(tipMoving)
	if sec.DBPass == "" {
		moveFailed(migrationStatus{Code: codeSource, Reason: "secrets.env lacks the old database's password"})
		return
	}
	if missing := missingPostgres(); missing != "" {
		moveFailed(migrationStatus{Code: codeSource, Reason: noPostgresReason, Detail: missing})
		return
	}
	r, err := reachPostgres()
	if err != nil {
		moveFailed(migrationStatus{Code: codeOther, Reason: "the bundled PostgreSQL cannot reach the old database's folder", Detail: err.Error()})
		return
	}
	defer r.remove()
	port, err := pickPort(pgPortFrom)
	if err != nil {
		moveFailed(migrationStatus{Code: codeOther, Reason: "there is no free local port for the old database", Detail: err.Error()})
		return
	}
	pgPort = port
	rotateLog(dpath("pglog.txt"), 10<<20)
	// pg_ctl -w succeeds only once the server it started is ready, which it is only after taking
	// 127.0.0.1:port itself: something else answering there is not handed the password. -t 300: a
	// server recovering from a hard stop can take longer than the default 60 s.
	pgctl := pgCmd(r, "pg_ctl.exe", "-D", filepath.Join(r.data, "pgdata"), "-l", filepath.Join(r.data, "pglog.txt"),
		"-o", "-p "+port+" -h 127.0.0.1 -c default_transaction_read_only=on -c autovacuum=off", "-w", "-t", "300", "start")
	if err := runToEnd(pgctl, &dbStarting); err != nil {
		if isStopping() {
			return
		}
		stopDatabase() // one pg_ctl gave up waiting for
		moveFailed(migrationStatus{Code: codeSource, Reason: "the old database did not start", Detail: err.Error() + pglogTail()})
		return
	}
	logf("old data: PostgreSQL started read-only on 127.0.0.1:%s", port)
	prepare := []string{"prepare", "--db", dbPath()}
	if merge {
		prepare = append(prepare, "--merge")
	}
	before, _ := os.ReadFile(statusPath())
	code, err := runMigrator([]string{"FORGE_MIGRATE_PG=" + oldDatabaseURL(port)}, prepare...)
	stopped := stopDatabase()
	switch {
	case isStopping():
		logf("old data: stopped before the move was finished; the next start moves it")
		return
	case !migratorDone(code, err, before):
		return
	case !stopped:
		moveFailed(migrationStatus{Code: codeRefused, Reason: "the old database did not shut down cleanly", Detail: pglogTail()})
		return
	}
	before, _ = os.ReadFile(statusPath())
	code, err = runMigrator(nil, "commit", "--db", dbPath(), "--pgdata", dpath("pgdata"))
	if isStopping() || !migratorDone(code, err, before) {
		return
	}
	st, _, _ := readStatus()
	if action, _, verified := quickPlan(dbPath(), dpath("pgdata")); st.State != stateDone || action != planNone || !verified {
		logf("old data: the move reports done, yet the next start would not take it for done (status %q, plan %q)", st.State, action)
		return
	}
	logf("old data: moved into forgesolo.db and checked")
	removeMoveTools()
}

// noPostgresReason is why a move fails when the bundled PostgreSQL is not installed: the launcher
// removed it after a move (and forgesolo.db was deleted since), or the old data was copied in after
// an install that had none. The installer installs it for an account with old data.
const noPostgresReason = "the bundled PostgreSQL, which the move needs, is not installed: run the Forge Solo installer again (it installs PostgreSQL when the old data is there), then start Forge Solo"

// missingPostgres names the bundled PostgreSQL programs a move runs that are not installed, or is
// "" when both are there.
func missingPostgres() string {
	var missing []string
	for _, name := range []string{"pg_ctl.exe", "postgres.exe"} {
		if _, err := os.Stat(ipath("pgsql", "bin", name)); errors.Is(err, fs.ErrNotExist) {
			missing = append(missing, ipath("pgsql", "bin", name))
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return "not found: " + strings.Join(missing, ", ")
}

// migratorDone reports whether a step of the migrator succeeded. Deferred (31: something holds
// forgesolo.db) is no failure: the migrator recorded it, the start goes on with the database as it
// is, and the next start finishes the move. A failure the migrator recorded itself is kept as it
// wrote it; any other is recorded here.
func migratorDone(code int, err error, before []byte) bool {
	switch {
	case err != nil:
		moveFailed(migrationStatus{Code: codeOther, Reason: migrateExe + " did not finish", Detail: err.Error() + pglogTail()})
		return false
	case code == 0:
		return true
	case code == codeDeferred:
		logf("old data: forgesolo.db is in use; the move finishes at the next start")
		return false
	}
	now, _ := os.ReadFile(statusPath())
	if st, _, _ := readStatus(); st.State == stateFailed && !bytes.Equal(now, before) {
		logf("old data: %s stopped with exit code %d: %s", migrateExe, code, st.Reason)
		setTrouble("database", tipMoveFailed)
		return false
	}
	reason := fmt.Sprintf("%s stopped with exit code %d", migrateExe, code)
	if code == codeInterrupted {
		reason = "the move was interrupted; nothing was replaced"
	}
	moveFailed(migrationStatus{Code: code, Reason: reason, Detail: pglogTail()})
	return false
}

// moveFailed records a move that was needed and failed, and says so in the tray until the next
// start. Nothing was replaced.
func moveFailed(s migrationStatus) {
	s.State = stateFailed
	s.Detail = strings.TrimSpace(s.Detail)
	recordStatus(s)
	setTrouble("database", tipMoveFailed)
}

// oldDatabaseURL is the old database's address for forge-solo-migrate, which it reads from its
// environment: on its command line other programs could read the password.
func oldDatabaseURL(port string) string {
	u := url.URL{Scheme: "postgres", User: url.UserPassword("forge", sec.DBPass), Host: "127.0.0.1:" + port,
		Path: "/forgesolo", RawQuery: "sslmode=disable&connect_timeout=30"}
	return u.String()
}

// errTooLong is a migrator step that did not end within migrateLimit.
var errTooLong = errors.New("it did not finish in time")

// runMigrator runs forge-solo-migrate.exe with args, and env added to its environment, under the
// key "migrate": Quit and Windows ending the session end it, and it is never started again. What it
// prints goes to launcher.log. It returns its exit code.
func runMigrator(env []string, args ...string) (int, error) {
	out, err := os.CreateTemp(dataDir, "migrate-*.log")
	if err != nil {
		return -1, err
	}
	defer func() {
		out.Close()
		os.Remove(out.Name())
	}()
	c := hidden(migrateExe, args...)
	c.Env = append(os.Environ(), env...)
	c.Stdout, c.Stderr = out, out
	done, err := startTracked("migrate", c, nil)
	if err != nil {
		return -1, err
	}
	finished := waitDone(done, migrateLimit)
	if finished {
		// It is no longer running: the next step may start at once, under the same key.
		mu.Lock()
		if procs["migrate"] == c {
			delete(procs, "migrate")
			delete(exited, "migrate")
		}
		mu.Unlock()
	} else {
		stop("migrate")
	}
	if b, err := os.ReadFile(out.Name()); err == nil {
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		for _, l := range lines[max(0, len(lines)-100):] {
			if l = strings.TrimSpace(l); l != "" {
				logf("%s %s: %s", migrateExe, args[0], l)
			}
		}
	}
	if !finished {
		return -1, fmt.Errorf("%s %s: %w (%v)", migrateExe, args[0], errTooLong, migrateLimit)
	}
	if c.ProcessState == nil {
		return -1, fmt.Errorf("%s %s: no exit status", migrateExe, args[0])
	}
	return c.ProcessState.ExitCode(), nil
}

// pglogTail is the end of PostgreSQL's log, for the status file's detail.
func pglogTail() string {
	b, err := os.ReadFile(dpath("pglog.txt"))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	return "\npglog.txt:\n" + strings.Join(lines[max(0, len(lines)-20):], "\n")
}

// removeMoveTools deletes what only a move needs, once it is done and checked: the bundled
// PostgreSQL programs (never the old data, pgdata) and PostgreSQL's log. Going back to 1.0.12 and
// forward again puts them back: the installer installs PostgreSQL for an account with old data. A
// move needed again later (forgesolo.db deleted) fails with noPostgresReason, which says to run the
// installer again.
func removeMoveTools() {
	if installDir != "" {
		pg := ipath("pgsql")
		if _, err := os.Lstat(pg); err == nil {
			if err := os.RemoveAll(pg); err != nil {
				logf("old data: %s, which only the move needed, could not be removed: %v", pg, err)
			} else {
				logf("old data: removed %s, which only the move needed", pg)
			}
		}
	}
	for _, f := range []string{"pglog.txt", "pglog.txt.1"} {
		if err := os.Remove(dpath(f)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			logf("old data: %s could not be removed: %v", f, err)
		}
	}
}
