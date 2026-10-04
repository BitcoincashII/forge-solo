//go:build sqlite

package main

import (
	"bytes"
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
	web, err := filepath.Abs(filepath.Join("..", "..", "web", "dist"))
	if err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), apiMainEnv+"=1", "DB_PATH="+db, "API_LISTEN_HOST=127.0.0.1", "API_LISTEN_PORT="+port,
		"HOME_APP=1", "SETTINGS_PASSWORD="+testPassword, "WEB_ROOT="+web, "FORGE_PLATFORM=umbrel",
		"RPC_URL=http://127.0.0.1:9", "STRATUM_INTERNAL_URL=http://127.0.0.1:9", "API_RATE_LIMIT=", "CORS_ORIGINS=")
	out := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	running := true
	defer func() {
		if running {
			cmd.Process.Kill()
			<-exited
		}
	}()

	base := "http://127.0.0.1:" + port
	client := &http.Client{Timeout: 5 * time.Second}
	do := func(method, path, body string, header map[string]string) (int, map[string]any, string) {
		t.Helper()
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, base+path, r)
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var j map[string]any
		_ = json.Unmarshal(b, &j)
		return resp.StatusCode, j, string(b)
	}

	// Up: the health check answers.
	var code int
	var health map[string]any
	var raw string
	for deadline := time.Now().Add(60 * time.Second); ; {
		if code, health, raw = do("GET", "/api/v1/health", "", nil); code != 0 {
			break
		}
		select {
		case err := <-exited:
			running = false
			t.Fatalf("MAINT-API-START: the api ended (%v) before it answered:\n%s", err, out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("MAINT-API-START: the api did not answer in 60 s:\n%s", out.String())
		}
		time.Sleep(100 * time.Millisecond)
	}

	if code != 200 || health["status"] != "maintenance" || health["code"] != float64(20) || health["reason"] != "the copy of the old data did not check out" {
		t.Errorf("MAINT-API-HEALTH: health answers %d %s, want 200 with status maintenance, code 20 and the reason", code, raw)
	}
	if c, j, b := do("GET", "/api/v1/blocks", "", nil); c != 503 || j["maintenance"] != true {
		t.Errorf("MAINT-API-503: /api/v1/blocks answers %d %s, want 503 maintenance", c, b)
	}
	if c, _, b := do("GET", "/settings", "", nil); c != 200 || !strings.Contains(b, "<title>Settings - Forge Solo</title>") {
		t.Errorf("MAINT-API-PAGES: the Settings page answers %d in maintenance, want the page", c)
	}
	if c, _, b := do("POST", "/api/v1/old-data", `{"skip":true}`, nil); c != 401 || skipFileThere(db) {
		t.Errorf("MAINT-API-PW: starting without the old data needs no password: %d %s", c, b)
	}
	if c, j, b := do("POST", "/api/v1/old-data", `{"skip":true}`, map[string]string{settingsPasswordHeader: testPassword}); c != 200 || j["success"] != true || !skipFileThere(db) {
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
	case err := <-exited:
		running = false
		if err != nil {
			t.Errorf("MAINT-API-EXIT: the api ended with %v, want exit code 0:\n%s", err, out.String())
		}
		if !strings.Contains(out.String(), "The move of the old data is no longer failed") {
			t.Errorf("MAINT-API-EXIT-LOG: the api did not say why it stopped:\n%s", out.String())
		}
	case <-time.After(15*time.Second - time.Since(changed)):
		t.Fatalf("MAINT-API-EXIT: the api still runs 15 s after the move stopped failing:\n%s", out.String())
	}
	if got := strings.Join(folderHolds(t, data), " "); got != "SKIP-POSTGRES-MIGRATION migration-status.json" {
		t.Errorf("MAINT-API-NODB: after maintenance the database folder holds %s", got)
	}
}
