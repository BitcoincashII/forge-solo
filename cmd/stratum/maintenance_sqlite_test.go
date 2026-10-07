package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
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

// The stratum as a program after a failed move: a copy of this test binary that runs main() with
// the config and environment the test gives it, the waits shortened.
const stratumMainEnv = "FORGE_STRATUM_TEST_MAIN"

func init() {
	if os.Getenv(stratumMainEnv) != "1" {
		return
	}
	if d, err := time.ParseDuration(os.Getenv("FORGE_STRATUM_TEST_POLL")); err == nil {
		moveStatusPollEvery = d
	}
	if d, err := time.ParseDuration(os.Getenv("FORGE_STRATUM_TEST_LOG_EVERY")); err == nil {
		notMiningLogEvery = d
	}
	main()
	os.Exit(0)
}

// outputBuffer is a program's output, written by its copiers and read by the test.
type outputBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (o *outputBuffer) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.Write(p)
}

func (o *outputBuffer) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.String()
}

// unusedPort is a TCP port on 127.0.0.1 that nothing listens on.
func unusedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

func listening(port string) bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 200*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// stratumProgram is a stratum started on a database whose move failed.
type stratumProgram struct {
	data, db             string
	minerPort, statsPort string
	out                  *outputBuffer
	stdin                io.WriteCloser
	exited               chan error
}

// startAfterFailedMove starts the stratum with a config it would mine with: without the status
// file it listens on minerPort. env is added to its environment.
func startAfterFailedMove(t *testing.T, env ...string) *stratumProgram {
	t.Helper()
	dir := t.TempDir()
	p := &stratumProgram{data: filepath.Join(dir, "data"), minerPort: unusedPort(t), statsPort: unusedPort(t), out: &outputBuffer{}, exited: make(chan error, 1)}
	p.db = filepath.Join(p.data, "forgesolo.db")
	if err := os.MkdirAll(p.data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := migstatus.Write(p.db, migstatus.Status{State: migstatus.Failed, Code: 30, Reason: "the old database's server did not shut down cleanly", Version: "1.0.13"}); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf(`stratum:
  host: 127.0.0.1
  port: %s
  extranonce1_size: 4
  extranonce2_size: 8
node:
  host: 127.0.0.1
  port: 9
  zmq_endpoint: ""
pool:
  payout_scheme: solo
logging:
  level: info
  format: json
`, p.minerPort)), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-config", config)
	cmd.Env = append(os.Environ(), stratumMainEnv+"=1", "FORGE_STRATUM_TEST_POLL=200ms", "FORGE_STRATUM_TEST_LOG_EVERY=300ms",
		"DB_PATH="+p.db, "INTERNAL_STATS_HOST=127.0.0.1", "INTERNAL_STATS_PORT="+p.statsPort, "RPC_USER=forge", "RPC_PASSWORD=x",
		"FORGE_STOP_ON_STDIN_EOF=")
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdout, cmd.Stderr = p.out, p.out
	var err error
	if p.stdin, err = cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.exited <- cmd.Wait() }()
	running := true
	t.Cleanup(func() {
		if running {
			cmd.Process.Kill()
			<-p.exited
		}
	})

	// Started: it says it is not mining, or it is listening for miners.
	for deadline := time.Now().Add(60 * time.Second); !strings.Contains(p.out.String(), notMiningLine) && !listening(p.minerPort); {
		select {
		case err := <-p.exited:
			running = false
			p.exited <- err
			t.Fatalf("MAINT-STRATUM-START: the stratum ended (%v) early:\n%s", err, p.out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("MAINT-STRATUM-START: in 60 s the stratum neither listened nor said it is not mining:\n%s", p.out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	return p
}

// ends waits up to wait for the program to end, and says how.
func (p *stratumProgram) ends(t *testing.T, wait time.Duration, code, what string) {
	t.Helper()
	select {
	case err := <-p.exited:
		p.exited <- err // for the cleanup
		if err != nil {
			t.Errorf("%s: the stratum ended with %v, want exit code 0:\n%s", code, err, p.out.String())
		}
	case <-time.After(wait):
		t.Fatalf("%s: the stratum still runs %v after %s:\n%s", code, wait, what, p.out.String())
	}
}

func TestStratumDoesNotMineAfterAFailedMove(t *testing.T) {
	p := startAfterFailedMove(t)
	if listening(p.minerPort) || listening(p.statsPort) {
		t.Fatalf("MAINT-STRATUM-LISTENS: the stratum listens (miners %v, stats %v) after a failed move", listening(p.minerPort), listening(p.statsPort))
	}
	if ents, _ := os.ReadDir(p.data); len(ents) != 1 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		t.Fatalf("MAINT-STRATUM-DB: the stratum made files beside the database: %s", strings.Join(names, " "))
	}
	time.Sleep(time.Second)
	if n := strings.Count(p.out.String(), notMiningLine); n < 2 {
		t.Errorf("MAINT-STRATUM-LOG: %q logged %d times in over a second at one every 300 ms:\n%s", notMiningLine, n, p.out.String())
	}

	// The move's next outcome ends the wait, with exit code 0.
	if err := migstatus.Write(p.db, migstatus.Status{State: migstatus.Degraded, Reason: "the old database's pg_control is missing", Version: "1.0.13"}); err != nil {
		t.Fatal(err)
	}
	p.ends(t, 5*time.Second, "MAINT-STRATUM-EXIT", "the move stopped failing")
	if _, err := os.Stat(p.db); err == nil {
		t.Error("MAINT-STRATUM-DB: the stratum made the database")
	}
}

// The Windows launcher stops the stratum by closing its stdin: that holds while it waits too.
func TestStratumWaitingAfterAFailedMoveStopsWhenAsked(t *testing.T) {
	p := startAfterFailedMove(t, "FORGE_STOP_ON_STDIN_EOF=1")
	p.stdin.Close()
	p.ends(t, 5*time.Second, "MAINT-STRATUM-STOP", "its stdin closed")
	if !strings.Contains(p.out.String(), "Shutting down...") {
		t.Errorf("MAINT-STRATUM-STOP: the stratum did not say it was stopping:\n%s", p.out.String())
	}
}
