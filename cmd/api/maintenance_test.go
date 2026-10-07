package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/migstatus"
	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/gofiber/fiber/v2"
)

// The api's side of the move of an earlier version's data: maintenance mode after a failed move,
// the dashboard's note otherwise, and the old-data choice. main() itself, in maintenance, is
// tested as a program in maintenance_sqlite_test.go.

const testPassword = "0123456789abcdef"

// statusDB is a database path in a new folder, with the move's status s beside it (none when s
// is empty), and the api's globals pointing at it until the test ends.
func statusDB(t *testing.T, state string) string {
	t.Helper()
	db := filepath.Join(t.TempDir(), "forgesolo.db")
	if state != "" {
		if err := migstatus.Write(db, migstatus.Status{State: state, Code: 20, Reason: "the copy of the old data did not check out", Version: "1.0.13"}); err != nil {
			t.Fatal(err)
		}
	}
	prevDB, prevM := migrationDB, maintenance
	migrationDB, maintenance = db, nil
	t.Cleanup(func() { migrationDB, maintenance = prevDB, prevM })
	return db
}

// testApp is the api as main() builds it, as far as these tests need: the two gates, then
// maintenance mode's when m is not nil, then a route of each kind.
func testApp(m *maintenanceMode) *fiber.App {
	app := fiber.New()
	app.Use(rejectCrossSiteWrites)
	app.Use(settingsPasswordGate(testPassword, true))
	if m != nil {
		app.Use(m.gate)
	}
	api := app.Group("/api/v1")
	api.Get("/stats", func(c *fiber.Ctx) error { return c.SendString("reached the figures") })
	api.Get("/health", healthCheck)
	api.Post("/old-data", saveOldDataChoice)
	app.Get("/settings", func(c *fiber.Ctx) error { return c.SendString("<html>settings</html>") })
	return app
}

type answer struct {
	code int
	body string
	json map[string]any
}

func call(t *testing.T, app *fiber.App, method, path, body string, header map[string]string) answer {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	a := answer{code: resp.StatusCode, body: string(b)}
	_ = json.Unmarshal(b, &a.json)
	return a
}

func withPassword() map[string]string { return map[string]string{settingsPasswordHeader: testPassword} }

func TestMaintenanceAnswersTheAPI(t *testing.T) {
	db := statusDB(t, migstatus.Failed)
	st, blocked := migstatus.Blocked(db)
	if !blocked {
		t.Fatal("MAINT-BLOCKED: a failed move does not block the database")
	}
	t.Setenv("FORGE_PLATFORM", "windows")
	maintenance = newMaintenance(db, st)
	app := testApp(maintenance)

	h := call(t, app, "GET", "/api/v1/health", "", nil)
	if h.code != 200 || h.json["status"] != "maintenance" || h.json["code"] != float64(20) ||
		h.json["reason"] != "the copy of the old data did not check out" || h.json["platform"] != "windows" || h.json["skip_file"] != false {
		t.Fatalf("MAINT-HEALTH: health answers %d %s, want 200, status maintenance, code 20 and the reason", h.code, h.body)
	}
	if h.json["database_file"] != false {
		t.Errorf("MAINT-DBFILE-ABSENT: with no database there, health says database_file %v, want false", h.json["database_file"])
	}
	if a := call(t, app, "HEAD", "/api/v1/health", "", nil); a.code != 200 {
		t.Errorf("MAINT-HEALTH-HEAD: the healthcheck's HEAD answers %d, want 200", a.code)
	}
	for _, p := range []string{"/api/v1/stats", "/API/V1/STATS", "/api/v1/stats/", "/api/v1/miners/x/payouts", "/api"} {
		a := call(t, app, "GET", p, "", nil)
		if a.code != 503 || a.json["maintenance"] != true || a.json["message"] != maintenanceMessage || a.json["error"] != maintenanceMessage {
			t.Errorf("MAINT-503: %s answers %d %s, want 503 with maintenance and the message", p, a.code, a.body)
		}
	}
	if a := call(t, app, "GET", "/settings", "", nil); a.code != 200 || !strings.Contains(a.body, "settings") {
		t.Errorf("MAINT-PAGES: the Settings page answers %d %q in maintenance, want the page", a.code, a.body)
	}

	// Start without the old data: the settings password, and the cross-site gate, as any change.
	if a := call(t, app, "POST", "/api/v1/old-data", `{"skip":true}`, nil); a.code != 401 {
		t.Errorf("MAINT-PW: old-data without the password answers %d, want 401", a.code)
	}
	if a := call(t, app, "POST", "/api/v1/old-data", `{"skip":true}`, map[string]string{settingsPasswordHeader: "wrong"}); a.code != 401 {
		t.Errorf("MAINT-PW: old-data with a wrong password answers %d, want 401", a.code)
	}
	if a := call(t, app, "POST", "/api/v1/old-data", `{"skip":true}`, map[string]string{settingsPasswordHeader: testPassword, "Sec-Fetch-Site": "cross-site"}); a.code != 403 {
		t.Errorf("MAINT-CSRF: old-data from another site answers %d, want 403", a.code)
	}
	if skipFileThere(db) {
		t.Fatal("MAINT-PW: a refused request wrote the skip file")
	}
	if a := call(t, app, "POST", "/api/v1/old-data", `{"skip":"yes"}`, withPassword()); a.code != 400 || skipFileThere(db) {
		t.Errorf("MAINT-SKIP-BODY: a body that is not a choice answers %d, want 400 and nothing written", a.code)
	}
	a := call(t, app, "POST", "/api/v1/old-data", `{"skip":true}`, withPassword())
	if a.code != 200 || a.json["success"] != true {
		t.Fatalf("MAINT-SKIP: old-data with the password answers %d %s", a.code, a.body)
	}
	if msg, _ := a.json["message"].(string); !strings.HasPrefix(msg, "Restart Forge Solo: it then starts with a new, empty database") || strings.Contains(msg, "already has") {
		t.Errorf("MAINT-SKIP-EMPTY: with no database there, the answer is %q, want a new, empty database", msg)
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(db), "SKIP-POSTGRES-MIGRATION"))
	if err != nil || string(b) != skipFileText {
		t.Fatalf("MAINT-SKIP: the skip file holds %q (%v)", b, err)
	}
	if h := call(t, app, "GET", "/api/v1/health", "", nil); h.json["skip_file"] != true {
		t.Errorf("MAINT-SKIP-SHOWN: after the choice, health says skip_file %v", h.json["skip_file"])
	}
	// Taken back before the restart: the move is tried again.
	if a := call(t, app, "POST", "/api/v1/old-data", `{"skip":false}`, withPassword()); a.code != 200 || skipFileThere(db) || a.json["message"] != "Restart Forge Solo to try the move again." {
		t.Errorf("MAINT-UNSKIP: taking the choice back answers %d %s, skip file there: %v", a.code, a.body, skipFileThere(db))
	}
	if _, err := os.Stat(db); err == nil {
		t.Error("MAINT-NODB: maintenance mode made a database")
	}
}

// With a database already there, starting without the old data starts on it as it is, with the
// payout address saved in it, not on a new, empty one. That is a merge that was needed and failed
// (1.0.12 ran again after the move, and may hold a newer payout address), or a move that failed
// while the api ran on its database. The health answer says the database is there, and the
// choice's answer says what then happens.
func TestStartingWithoutTheOldDataOnTheDatabaseThere(t *testing.T) {
	content := []byte("SQLite format 3\x00 the database 1.0.13 used before going back to 1.0.12")
	dbThere := func() string {
		t.Helper()
		db := statusDB(t, migstatus.Failed)
		if err := os.WriteFile(db, content, 0o600); err != nil {
			t.Fatal(err)
		}
		return db
	}
	saysTheDatabaseThere := func(code string, a answer) {
		t.Helper()
		msg, _ := a.json["message"].(string)
		for _, want := range []string{"Restart Forge Solo: it then starts on the database it already has, as it is,",
			"If that database holds a payout address, Forge Solo mines to it as soon as it starts",
			"check the payout address in Settings right after the restart"} {
			if a.code != 200 || !strings.Contains(msg, want) {
				t.Errorf("%s: starting without the old data answers %d %q, lacking %q", code, a.code, msg, want)
			}
		}
		if strings.Contains(msg, "empty") {
			t.Errorf("%s: with a database there, the answer speaks of an empty one: %q", code, msg)
		}
	}

	db := dbThere()
	st, _ := migstatus.Blocked(db)
	maintenance = newMaintenance(db, st)
	app := testApp(maintenance)
	if h := call(t, app, "GET", "/api/v1/health", "", nil); h.code != 200 || h.json["status"] != "maintenance" || h.json["database_file"] != true {
		t.Errorf("MAINT-DBFILE-THERE: with forgesolo.db there, health answers %d %s, want maintenance with database_file true", h.code, h.body)
	}
	saysTheDatabaseThere("OLDDATA-SKIP-THERE", call(t, app, "POST", "/api/v1/old-data", `{"skip":true}`, withPassword()))
	if b, err := os.ReadFile(db); err != nil || string(b) != string(content) || !skipFileThere(db) {
		t.Errorf("OLDDATA-SKIP-THERE: the database there changed (%v), or no skip file was written", err)
	}

	// The move failed while the api ran normally on its database.
	dbThere()
	saysTheDatabaseThere("OLDDATA-SKIP-RUNNING", call(t, testApp(nil), "POST", "/api/v1/old-data", `{"skip":true}`, withPassword()))
}

// The status file is read again while the api is in maintenance: a new failure is what health
// shows, and the end of the failure ends maintenance mode.
func TestMaintenanceFollowsTheStatusFile(t *testing.T) {
	db := statusDB(t, migstatus.Failed)
	st, _ := migstatus.Blocked(db)
	m := newMaintenance(db, st)
	go m.watch(20 * time.Millisecond)
	app := testApp(m)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(migstatus.Write(db, migstatus.Status{State: migstatus.Failed, Code: 10, Reason: "the old database could not be reached"}))
	for i := 0; ; i++ {
		h := call(t, app, "GET", "/api/v1/health", "", nil)
		if h.json["code"] == float64(10) && h.json["reason"] == "the old database could not be reached" {
			break
		}
		if i > 200 {
			t.Fatalf("MAINT-REREAD: health still says %s after the status file changed", h.body)
		}
		select {
		case <-m.done():
			t.Fatal("MAINT-REREAD: a new failure ended maintenance mode")
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	must(migstatus.Write(db, migstatus.Status{State: migstatus.Skipped}))
	select {
	case <-m.done():
	case <-time.After(5 * time.Second):
		t.Fatal("MAINT-SETTLED: maintenance mode did not end when the move was no longer failed")
	}
	var nilMode *maintenanceMode
	if nilMode.done() != nil {
		t.Error("MAINT-NORMAL: an api that runs normally has an end of maintenance to wait for")
	}
}

// Running normally, the health answer carries the move's state when the dashboard has to say
// something about it, and nothing new otherwise.
func TestHealthCarriesTheMovesState(t *testing.T) {
	for _, state := range []string{migstatus.Deferred, migstatus.Degraded, migstatus.Skipped, migstatus.Failed} {
		statusDB(t, state)
		h := call(t, testApp(nil), "GET", "/api/v1/health", "", nil)
		m, _ := h.json["migration"].(map[string]any)
		if h.code != 200 || m == nil || m["state"] != state || m["reason"] != "the copy of the old data did not check out" || m["skip_file"] != false {
			t.Errorf("HEALTH-MIGRATION: with a %s move health answers %d %s, want migration {state, reason, skip_file}", state, h.code, h.body)
		}
	}
	for _, state := range []string{migstatus.None, migstatus.Done} {
		statusDB(t, state)
		if h := call(t, testApp(nil), "GET", "/api/v1/health", "", nil); h.json["migration"] != nil {
			t.Errorf("HEALTH-MIGRATION-QUIET: with the move %s, health says %s", state, h.body)
		}
	}
}

// Without a status file (Forge Solo for Linux, and a PostgreSQL build) the health answer is what
// it always was, to the byte.
func TestHealthWithoutAStatusFileIsUnchanged(t *testing.T) {
	for _, noFile := range []bool{true, false} {
		statusDB(t, "")
		if noFile {
			migrationDB = "" // the PostgreSQL build
		}
		db := migrationDB
		got := call(t, testApp(nil), "GET", "/api/v1/health", "", nil)
		settingsMu.RLock()
		n := len(minerSettings)
		settingsMu.RUnlock()
		connected := stats.IsDBConnected()
		status := "healthy"
		if !connected {
			status = "degraded"
		}
		want, _ := json.Marshal(map[string]any{"status": status, "db_connected": connected, "settings_loaded": n})
		if got.code != 200 || got.body != string(want) {
			t.Errorf("HEALTH-UNCHANGED: with no status file (db %q) health answers %d %s, want %s", db, got.code, got.body, want)
		}
	}
}

// After the user chose to start without the old data, Settings can bring it in (and take that
// back before the restart). Anywhere else there is no choice to make.
func TestOldDataChoiceWhenRunning(t *testing.T) {
	db := statusDB(t, migstatus.Skipped)
	if err := os.WriteFile(migstatus.SkipPath(db), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := testApp(nil)
	if a := call(t, app, "POST", "/api/v1/old-data", `{"skip":false}`, nil); a.code != 401 || !skipFileThere(db) {
		t.Fatalf("OLDDATA-PW: bringing the old data in without the password answers %d", a.code)
	}
	a := call(t, app, "POST", "/api/v1/old-data", `{"skip":false}`, withPassword())
	if a.code != 200 || skipFileThere(db) || a.json["message"] != "Restart Forge Solo to bring the data of the earlier version in." {
		t.Fatalf("OLDDATA-UNSKIP: answers %d %s; skip file there: %v", a.code, a.body, skipFileThere(db))
	}
	if h := call(t, app, "GET", "/api/v1/health", "", nil); fmt.Sprint(h.json["migration"]) != "map[reason:the copy of the old data did not check out skip_file:false state:skipped]" {
		t.Errorf("OLDDATA-UNSKIP-SHOWN: health says %s after the skip file went", h.body)
	}
	if a := call(t, app, "POST", "/api/v1/old-data", `{"skip":true}`, withPassword()); a.code != 200 || !skipFileThere(db) {
		t.Fatalf("OLDDATA-RESKIP: taking it back answers %d %s", a.code, a.body)
	}

	for _, state := range []string{"", migstatus.None, migstatus.Done, migstatus.Deferred, migstatus.Degraded} {
		db := statusDB(t, state)
		for _, body := range []string{`{"skip":true}`, `{"skip":false}`} {
			if a := call(t, app, "POST", "/api/v1/old-data", body, withPassword()); a.code != 409 || skipFileThere(db) {
				t.Errorf("OLDDATA-NO-CHOICE: with the move %q, %s answers %d %s", state, body, a.code, a.body)
			}
		}
	}
	statusDB(t, migstatus.Skipped)
	migrationDB = ""
	if a := call(t, app, "POST", "/api/v1/old-data", `{"skip":true}`, withPassword()); a.code != 409 {
		t.Errorf("OLDDATA-NO-FILE: a build without a database file answers %d, want 409", a.code)
	}
	if _, err := os.Lstat(migstatus.SkipName); err == nil {
		t.Error("OLDDATA-NO-FILE: a skip file was written in the working folder")
	}
}

// The PostgreSQL build has no database file, so no status file, and never runs in maintenance.
func TestPostgresBuildHasNoStatusFile(t *testing.T) {
	t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "forgesolo.db"))
	pg := strings.HasPrefix(stats.GetDBConnStr(), "host=")
	if got := stats.DatabaseFile(); pg && got != "" || !pg && got != os.Getenv("DB_PATH") {
		t.Errorf("MAINT-DBFILE: DatabaseFile is %q (PostgreSQL build: %v)", got, pg)
	}
}
