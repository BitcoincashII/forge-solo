package main

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/migstatus"
	"github.com/gofiber/fiber/v2"
)

// Forge Solo 1.0.13 moves an earlier version's PostgreSQL data into forgesolo.db once
// (forge-solo-migrate on Umbrel, the launcher on Windows) and records what came of it in
// migration-status.json beside the database (internal/migstatus). Forge Solo for Linux never had
// PostgreSQL and has no such file.
//
// When the move was needed and failed, the api runs in maintenance mode. It opens no database, so
// none is made, empty, where the old data belongs, and:
//   - /api/v1/health answers 200 with status "maintenance" and the failure's code and reason: the
//     container's healthcheck passes, so the dashboard comes up and says what happened;
//   - POST /api/v1/old-data {"skip": true} writes SKIP-POSTGRES-MIGRATION, the choice to start
//     without the old data, behind the settings password as every other change. Forge Solo then
//     starts on a new, empty database, or on the one already there when a merge was needed
//     (database_file in the health answer);
//   - every other API request answers 503;
//   - the pages are served as usual.
//
// It reads the status file every statusPollEvery and ends, with exit code 0, once the move is no
// longer failed; Docker's restart policy, or the Windows launcher, then starts it normally. On an
// Umbrel that reboots, Docker starts the api before compose runs the move again, so the api starts
// in maintenance on the old status and has to notice the new one.

// migrationDB is the database whose status file the api reads (stats.DatabaseFile); "" where there
// is none, in the PostgreSQL build.
var migrationDB string

// statusPollEvery is how often maintenance mode reads the status file. A variable for the tests.
var statusPollEvery = 10 * time.Second

// maintenance is the api's maintenance mode; nil when it runs normally.
var maintenance *maintenanceMode

type maintenanceMode struct {
	db      string
	mu      sync.Mutex
	status  migstatus.Status // the failure, as last read
	settled chan struct{}    // closed once the move is no longer failed
}

// maintenanceMessage is every API answer but the health check's in maintenance mode.
const maintenanceMessage = "Forge Solo could not move the data of its earlier version into its new database, so it is not mining and shows no figures. Nothing was lost. The dashboard says what to do."

func newMaintenance(db string, st migstatus.Status) *maintenanceMode {
	return &maintenanceMode{db: db, status: st, settled: make(chan struct{})}
}

// watch reads the status file every `every` until the move is no longer failed, and then closes
// settled.
func (m *maintenanceMode) watch(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for range t.C {
		st, blocked := migstatus.Blocked(m.db)
		if !blocked {
			close(m.settled)
			return
		}
		m.mu.Lock()
		m.status = st
		m.mu.Unlock()
	}
}

// done is closed once the move is no longer failed; nil, which never is, when the api runs normally.
func (m *maintenanceMode) done() <-chan struct{} {
	if m == nil {
		return nil
	}
	return m.settled
}

// gate answers the API's requests in maintenance mode and lets the pages through. It comes after
// the cross-site and settings password gates, which a change has to pass as any other.
func (m *maintenanceMode) gate(c *fiber.Ctx) error {
	p := strings.TrimRight(strings.ToLower(c.Path()), "/")
	switch {
	case p == "/api/v1/health" && (c.Method() == fiber.MethodGet || c.Method() == fiber.MethodHead):
		m.mu.Lock()
		st := m.status
		m.mu.Unlock()
		return c.JSON(fiber.Map{"status": "maintenance", "code": st.Code, "reason": st.Reason,
			"platform": platformFromEnv(), "skip_file": skipFileThere(m.db), "database_file": databaseThere(m.db)})
	case p == "/api/v1/old-data" && c.Method() == fiber.MethodPost:
		return saveOldDataChoice(c)
	case p == "/api" || strings.HasPrefix(p, "/api/") || p == "/metrics" || p == "/health":
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"success": false, "maintenance": true,
			"message": maintenanceMessage, "error": maintenanceMessage})
	}
	return c.Next()
}

// migrationNote is what the health answer adds for the dashboard's banner when the move needs a
// word: it waits for the next start, the old data was left out, the old folder is damaged, or the
// move failed while the api was running. nil otherwise, and when there is no status file.
func migrationNote(db string) fiber.Map {
	if db == "" {
		return nil
	}
	st, ok, err := migstatus.Read(db)
	if err != nil || !ok {
		return nil
	}
	switch st.State {
	case migstatus.Deferred, migstatus.Degraded, migstatus.Skipped, migstatus.Failed:
		return fiber.Map{"state": st.State, "reason": st.Reason, "skip_file": skipFileThere(db)}
	}
	return nil
}

// skipFileThere reports whether SKIP-POSTGRES-MIGRATION is beside the database at db.
func skipFileThere(db string) bool {
	_, err := os.Lstat(migstatus.SkipPath(db))
	return err == nil
}

// databaseThere reports whether a database is at db, so that starting without the old data starts
// on it and not on a new, empty one. Unless it is certainly absent it counts as there: the text
// for that case is the one that warns about its payout address.
func databaseThere(db string) bool {
	_, err := os.Lstat(db)
	return !errors.Is(err, fs.ErrNotExist)
}

// What the answer to "start without the old data" says happens at the restart. With no database
// yet (the first move failed), Forge Solo starts on a new, empty one. With one there (a merge
// that was needed failed: 1.0.12 ran on the old data again after the move, and may hold a newer
// payout address), it starts on that one as it is and mines to the payout address saved in it.
const (
	skipOnANewDatabase     = "Restart Forge Solo: it then starts with a new, empty database, without the data of the earlier version. That data stays where it is, and Settings can bring it in later."
	skipOnTheDatabaseThere = "Restart Forge Solo: it then starts on the database it already has, as it is, without what the earlier version recorded that is not in it. If that database holds a payout address, Forge Solo mines to it as soon as it starts, so check the payout address in Settings right after the restart. The old data stays where it is, and Settings can bring it in later."
)

// skipFileText is SKIP-POSTGRES-MIGRATION's content, for whoever finds it.
const skipFileText = "Forge Solo starts without the data of its earlier version while this file is here.\n" +
	"Delete it, or use Settings, and restart Forge Solo to bring that data in.\n"

// saveOldDataChoice is POST /api/v1/old-data {"skip": true|false}: start without the earlier
// version's data (SKIP-POSTGRES-MIGRATION beside the database), or bring it in. Either takes effect
// when Forge Solo next starts. Only while the move has failed or the old data was left out.
func saveOldDataChoice(c *fiber.Ctx) error {
	var in struct {
		Skip *bool `json:"skip"`
	}
	if err := c.BodyParser(&in); err != nil || in.Skip == nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "error": `Send {"skip": true} or {"skip": false}. Nothing was changed.`})
	}
	state := ""
	switch {
	case maintenance != nil:
		state = migstatus.Failed
	case migrationDB != "":
		if st, ok, err := migstatus.Read(migrationDB); ok && err == nil {
			state = st.State
		}
	}
	if state != migstatus.Failed && state != migstatus.Skipped {
		return c.Status(fiber.StatusConflict).JSON(fiber.Map{"success": false,
			"error": "Forge Solo is not waiting for a choice about the data of its earlier version. Nothing was changed."})
	}

	skip := migstatus.SkipPath(migrationDB)
	var err error
	if *in.Skip {
		err = migstatus.WriteDurably(skip, []byte(skipFileText))
	} else if err = os.Remove(skip); err == nil {
		err = migstatus.SyncDir(filepath.Dir(skip))
	} else if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	if err != nil {
		log.Printf("old data: the choice could not be recorded: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"success": false,
			"error": "Forge Solo could not record the choice (" + err.Error() + "). Nothing was changed."})
	}
	did := "removed"
	if *in.Skip {
		did = "wrote"
	}
	log.Printf("old data: %s %s (the move: %s)", did, skip, state)

	var msg string
	switch {
	case *in.Skip && state == migstatus.Failed && databaseThere(migrationDB):
		msg = skipOnTheDatabaseThere
	case *in.Skip && state == migstatus.Failed:
		msg = skipOnANewDatabase
	case *in.Skip:
		msg = "Forge Solo goes on starting without the data of the earlier version."
	case state == migstatus.Failed:
		msg = "Restart Forge Solo to try the move again."
	default:
		msg = "Restart Forge Solo to bring the data of the earlier version in."
	}
	return c.JSON(fiber.Map{"success": true, "message": msg})
}
