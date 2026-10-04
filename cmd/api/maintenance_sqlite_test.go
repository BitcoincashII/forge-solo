//go:build sqlite

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/migstatus"
	"github.com/BitcoincashII/forge-solo/internal/stats"
)

// The api as a program after a failed move: a copy of this test binary that runs main() with the
// environment the test gives it, as Umbrel's compose and the Windows launcher start it.
const apiMainEnv = "FORGE_API_TEST_MAIN"

func init() {
	if os.Getenv(apiMainEnv) != "1" {
		return
	}
	main()
	os.Exit(0)
}

// lockedBuffer is a program's output, written by its copiers and read by the test.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// freePort is a TCP port on 127.0.0.1 that nothing listens on.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

// folderHolds is the names in dir, sorted.
func folderHolds(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// fileSum is the sha256 of the file at path.
func fileSum(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// apiProgram is the api's main() running in a copy of this test binary.
type apiProgram struct {
	t       *testing.T
	cmd     *exec.Cmd
	base    string
	out     *lockedBuffer
	exited  chan error
	running bool
}

// startAPI starts the api on the database db, as Umbrel's compose starts it, and waits until its
// health check answers. The test's end stops it.
func startAPI(t *testing.T, db string) *apiProgram {
	t.Helper()
	web, err := filepath.Abs(filepath.Join("..", "..", "web", "dist"))
	if err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	p := &apiProgram{t: t, cmd: exec.Command(os.Args[0]), base: "http://127.0.0.1:" + port, out: &lockedBuffer{}, exited: make(chan error, 1)}
	p.cmd.Env = append(os.Environ(), apiMainEnv+"=1", "DB_PATH="+db, "API_LISTEN_HOST=127.0.0.1", "API_LISTEN_PORT="+port,
		"HOME_APP=1", "SETTINGS_PASSWORD="+testPassword, "WEB_ROOT="+web, "FORGE_PLATFORM=umbrel",
		"RPC_URL=http://127.0.0.1:9", "STRATUM_INTERNAL_URL=http://127.0.0.1:9", "API_RATE_LIMIT=", "CORS_ORIGINS=")
	p.cmd.Stdout, p.cmd.Stderr = p.out, p.out
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p.running = true
	go func() { p.exited <- p.cmd.Wait() }()
	t.Cleanup(p.kill)

	for deadline := time.Now().Add(60 * time.Second); ; {
		if code, _, _ := p.do("GET", "/api/v1/health", "", nil); code != 0 {
			return p
		}
		select {
		case err := <-p.exited:
			p.running = false
			t.Fatalf("MAINT-API-START: the api ended (%v) before it answered:\n%s", err, p.out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("MAINT-API-START: the api did not answer in 60 s:\n%s", p.out.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// kill stops the api if it still runs.
func (p *apiProgram) kill() {
	if p.running {
		p.cmd.Process.Kill()
		<-p.exited
		p.running = false
	}
}

// do sends a request to the api: its status (0 when it did not answer), its JSON and its body.
func (p *apiProgram) do(method, path, body string, header map[string]string) (int, map[string]any, string) {
	p.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, p.base+path, r)
	if err != nil {
		p.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var j map[string]any
	_ = json.Unmarshal(b, &j)
	return resp.StatusCode, j, string(b)
}

func TestMaintenanceModeAsAProgram(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(data, "forgesolo.db")
	if err := migstatus.Write(db, migstatus.Status{State: migstatus.Failed, Code: 20, Reason: "the copy of the old data did not check out",
		Detail: "blocks: 14 rows in PostgreSQL, 13 in the new database", Version: "1.0.13"}); err != nil {
		t.Fatal(err)
	}
	api := startAPI(t, db)

	if code, health, raw := api.do("GET", "/api/v1/health", "", nil); code != 200 || health["status"] != "maintenance" || health["code"] != float64(20) || health["reason"] != "the copy of the old data did not check out" {
		t.Errorf("MAINT-API-HEALTH: health answers %d %s, want 200 with status maintenance, code 20 and the reason", code, raw)
	}
	if c, j, b := api.do("GET", "/api/v1/blocks", "", nil); c != 503 || j["maintenance"] != true {
		t.Errorf("MAINT-API-503: /api/v1/blocks answers %d %s, want 503 maintenance", c, b)
	}
	if c, _, b := api.do("GET", "/settings", "", nil); c != 200 || !strings.Contains(b, "<title>Settings - Forge Solo</title>") {
		t.Errorf("MAINT-API-PAGES: the Settings page answers %d in maintenance, want the page", c)
	}
	if c, _, b := api.do("POST", "/api/v1/old-data", `{"skip":true}`, nil); c != 401 || skipFileThere(db) {
		t.Errorf("MAINT-API-PW: starting without the old data needs no password: %d %s", c, b)
	}
	if c, j, b := api.do("POST", "/api/v1/old-data", `{"skip":true}`, map[string]string{settingsPasswordHeader: testPassword}); c != 200 || j["success"] != true || !skipFileThere(db) {
		t.Errorf("MAINT-API-SKIP: with the password, old-data answers %d %s; skip file there: %v", c, b, skipFileThere(db))
	}
	// No database, nor its in-use lock: nothing was opened.
	if got := strings.Join(folderHolds(t, data), " "); got != "SKIP-POSTGRES-MIGRATION migration-status.json" {
		t.Errorf("MAINT-API-NODB: in maintenance the database folder holds %s", got)
	}

	// The next start of the move records another outcome: the api ends, to be started normally.
	changed := time.Now()
	if err := migstatus.Write(db, migstatus.Status{State: migstatus.Skipped, Version: "1.0.13"}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-api.exited:
		api.running = false
		if err != nil {
			t.Errorf("MAINT-API-EXIT: the api ended with %v, want exit code 0:\n%s", err, api.out.String())
		}
		if !strings.Contains(api.out.String(), "The move of the old data is no longer failed") {
			t.Errorf("MAINT-API-EXIT-LOG: the api did not say why it stopped:\n%s", api.out.String())
		}
	case <-time.After(15*time.Second - time.Since(changed)):
		t.Fatalf("MAINT-API-EXIT: the api still runs 15 s after the move stopped failing:\n%s", api.out.String())
	}
	if got := strings.Join(folderHolds(t, data), " "); got != "SKIP-POSTGRES-MIGRATION migration-status.json" {
		t.Errorf("MAINT-API-NODB: after maintenance the database folder holds %s", got)
	}
}

// A merge that was needed failed: 1.0.13 had moved the old data, 1.0.12 ran on it again, and
// bringing in what it recorded since failed. Starting without the old data then starts on the
// database 1.0.13 already had, with the payout address saved in it, which mining uses at once.
// The api says so, opens nothing while in maintenance, and runs on that database after the
// restart.
func TestMaintenanceModeOnTheDatabaseThere(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(data, "forgesolo.db")
	const addr = "bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q"
	if err := stats.InitDB(db); err != nil {
		t.Fatal(err)
	}
	err := stats.SavePoolConfig(addr, "", "")
	stats.CloseDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "postgres-migrated.json"), []byte(`{"pg_control_sha256":"before 1.0.12 ran again"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migstatus.Write(db, migstatus.Status{State: migstatus.Failed, Code: 20, Reason: "the copy of the old data did not check out", Version: "1.0.13"}); err != nil {
		t.Fatal(err)
	}
	before := folderHolds(t, data)
	sum := fileSum(t, db)

	api := startAPI(t, db)
	if code, h, raw := api.do("GET", "/api/v1/health", "", nil); code != 200 || h["status"] != "maintenance" || h["database_file"] != true {
		t.Errorf("MAINT-API-DBFILE: with forgesolo.db there, health answers %d %s, want maintenance with database_file true", code, raw)
	}
	c, j, b := api.do("POST", "/api/v1/old-data", `{"skip":true}`, map[string]string{settingsPasswordHeader: testPassword})
	msg, _ := j["message"].(string)
	if c != 200 || !strings.HasPrefix(msg, "Restart Forge Solo: it then starts on the database it already has") ||
		!strings.Contains(msg, "check the payout address in Settings right after the restart") || strings.Contains(msg, "empty") {
		t.Errorf("MAINT-API-SKIP-THERE: starting without the old data answers %d %s, want the database already there and its payout address", c, b)
	}
	// Nothing was opened: the folder holds what it held, and the skip file.
	after := append(before[:len(before):len(before)], "SKIP-POSTGRES-MIGRATION")
	sort.Strings(after)
	want := strings.Join(after, " ")
	if got := strings.Join(folderHolds(t, data), " "); got != want || fileSum(t, db) != sum {
		t.Errorf("MAINT-API-THERE-UNTOUCHED: in maintenance the folder went from %v to %s (want %s), database unchanged: %v", before, got, want, fileSum(t, db) == sum)
	}
	api.kill()

	// The next start records the choice, and the api runs on that database, payout address and all.
	if err := migstatus.Write(db, migstatus.Status{State: migstatus.Skipped, Reason: "SKIP-POSTGRES-MIGRATION is there", Version: "1.0.13"}); err != nil {
		t.Fatal(err)
	}
	api = startAPI(t, db)
	if c, j, b := api.do("GET", "/api/v1/pool/config", "", nil); c != 200 || j["pool_address"] != addr || j["configured"] != true {
		t.Errorf("MAINT-API-THERE-RESTART: after the restart the payout settings are %d %s, want the database there with %s", c, b, addr)
	}
}
