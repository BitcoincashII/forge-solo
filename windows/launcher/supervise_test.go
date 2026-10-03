package main

import (
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// superviseWorld makes key a program the launcher starts again, started by a helper in mode, with
// short waits, and returns how many times it has been started. Everything is undone afterwards.
func superviseWorld(t *testing.T, key, mode string) *atomic.Int32 {
	t.Helper()
	startSupervising(t)
	savedData := dataDir
	dataDir = t.TempDir()
	dir := t.TempDir()
	starts := &atomic.Int32{}
	start := func() error {
		starts.Add(1)
		c := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
		c.Env = append(os.Environ(), "FS_HELPER="+mode, "FS_HELPER_DIR="+dir)
		return run(key, c)
	}
	restartMu.Lock()
	a, b, q := restartFirstWait, restartMaxWait, restartQuick
	restartFirstWait, restartMaxWait, restartQuick = 50*time.Millisecond, 200*time.Millisecond, time.Hour
	restartMu.Unlock()
	extraMu.Lock()
	extraSupervised[key] = struct {
		start func() error
		what  string
	}{start, "the test program"}
	extraMu.Unlock()
	t.Cleanup(func() {
		extraMu.Lock()
		delete(extraSupervised, key)
		extraMu.Unlock()
		stop(key)
		restartsUnderWay.Wait()
		stop(key)
		restartMu.Lock()
		delete(restartWaits, key)
		restartFirstWait, restartMaxWait, restartQuick = a, b, q
		restartMu.Unlock()
		mu.Lock()
		stopping = false
		mu.Unlock()
		dataDir = savedData
	})
	if err := start(); err != nil {
		t.Fatal(err)
	}
	return starts
}

func pidOf(key string) int {
	mu.Lock()
	defer mu.Unlock()
	if c := procs[key]; c != nil && c.Process != nil {
		return c.Process.Pid
	}
	return 0
}

// A program that crashes is started again, and the log says so: on Windows nothing else would, and
// the tray said "running" with nothing mining.
// TestMain runs the tests with programs that exit on their own left alone: most tests start
// stand-ins that exit at once, which would otherwise be started again under the next test. The
// tests of starting them again turn it on (superviseWorld, supervise).
func TestMain(m *testing.M) {
	supervising.Store(false)
	os.Exit(m.Run())
}

// startSupervising turns starting programs again on for the test.
func startSupervising(t *testing.T) {
	t.Helper()
	supervising.Store(true)
	t.Cleanup(func() { supervising.Store(false) })
}

func TestACrashedProgramIsStartedAgain(t *testing.T) {
	starts := superviseWorld(t, "crasher", "crash-once")
	for end := time.Now().Add(5 * time.Second); starts.Load() < 2 && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
	}
	time.Sleep(200 * time.Millisecond)
	if starts.Load() != 2 || pidOf("crasher") == 0 {
		t.Fatalf("SUPERVISE-RESTART: a program that crashed was started %d times and is running: %v", starts.Load(), pidOf("crasher") != 0)
	}
	b, _ := os.ReadFile(dpath("launcher.log"))
	if !strings.Contains(string(b), "exited on its own after") || !strings.Contains(string(b), "exit code 3") {
		t.Errorf("SUPERVISE-LOGGED: launcher.log does not say the program exited on its own, and how:\n%s", b)
	}
}

// A program the launcher stops -- Quit, Restart Mining, a node asked to stop -- is not started again.
func TestAStoppedProgramIsNotStartedAgain(t *testing.T) {
	starts := superviseWorld(t, "stopped", "stuck")
	time.Sleep(300 * time.Millisecond)
	stop("stopped")
	time.Sleep(500 * time.Millisecond)
	if starts.Load() != 1 || pidOf("stopped") != 0 {
		t.Fatalf("SUPERVISE-STOPPED: a program the launcher stopped was started again (%d starts)", starts.Load())
	}
}

// One that ends while Forge Solo is stopping is not started again either.
func TestNothingIsStartedAgainWhileStopping(t *testing.T) {
	starts := superviseWorld(t, "late", "stuck")
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	stopping = true
	c := procs["late"]
	mu.Unlock()
	_ = c.Process.Kill() // it ends on its own, as far as the launcher knows
	time.Sleep(500 * time.Millisecond)
	if starts.Load() != 1 {
		t.Fatalf("SUPERVISE-STOPPING: a program that ended while Forge Solo was stopping was started again (%d starts)", starts.Load())
	}
	if b, _ := os.ReadFile(dpath("launcher.log")); strings.Contains(string(b), "exited on its own") {
		t.Fatalf("SUPERVISE-STOPPING-LOG: a program ended during the stop is logged as having exited on its own:\n%s", b)
	}
}

// A program that keeps crashing at once is started again less and less often, up to the longest
// wait, rather than in a tight loop.
func TestTheWaitGrowsForAProgramThatKeepsCrashing(t *testing.T) {
	starts := superviseWorld(t, "looper", "exit3")
	time.Sleep(1500 * time.Millisecond)
	restartMu.Lock()
	wait := restartWaits["looper"]
	restartMu.Unlock()
	if wait != 200*time.Millisecond || starts.Load() > 12 {
		t.Fatalf("SUPERVISE-BACKOFF: after %d starts in 1.5 s the wait is %v, want it grown to 200ms", starts.Load(), wait)
	}
}

// pipedStart is a start for key that runs a helper in mode with a stdin pipe, as the miner runs.
func pipedStart(key, mode, dir string, starts *atomic.Int32) func() error {
	return func() error {
		starts.Add(1)
		c := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
		c.Env = append(os.Environ(), "FS_HELPER="+mode, "FS_HELPER_DIR="+dir)
		w, err := c.StdinPipe()
		if err != nil {
			return err
		}
		return runPiped(key, c, w)
	}
}

// supervise makes key a program started again by start, after 50 ms, until the test ends.
func supervise(t *testing.T, key string, start func() error) {
	t.Helper()
	startSupervising(t)
	restartMu.Lock()
	a, b, q := restartFirstWait, restartMaxWait, restartQuick
	restartFirstWait, restartMaxWait, restartQuick = 50*time.Millisecond, 200*time.Millisecond, time.Hour
	restartMu.Unlock()
	t.Cleanup(func() {
		restartMu.Lock()
		delete(restartWaits, key)
		restartFirstWait, restartMaxWait, restartQuick = a, b, q
		restartMu.Unlock()
	})
	extraMu.Lock()
	extraSupervised[key] = struct {
		start func() error
		what  string
	}{start, "the test program"}
	extraMu.Unlock()
	t.Cleanup(func() {
		extraMu.Lock()
		delete(extraSupervised, key)
		extraMu.Unlock()
		restartsUnderWay.Wait()
		stop(key)
	})
}

// The miner stopped gently (Quit, Restart Mining) exits by itself: that is not a crash.
func TestAMinerStoppedGentlyIsNotStartedAgain(t *testing.T) {
	saved := dataDir
	dataDir = t.TempDir()
	t.Cleanup(func() { dataDir = saved })
	starts := &atomic.Int32{}
	start := pipedStart("gentle", "eof", t.TempDir(), starts)
	supervise(t, "gentle", start)
	if err := start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	stopGracefully("gentle", 5*time.Second)
	time.Sleep(time.Second)
	if starts.Load() != 1 {
		t.Fatalf("SUPERVISE-GRACEFUL: a miner stopped gently was started again (%d starts)", starts.Load())
	}
}

// A node asked to stop exits by itself: that is not a crash either.
func TestNodesAskedToStopAreNotStartedAgain(t *testing.T) {
	saved := dataDir
	dataDir = t.TempDir()
	t.Cleanup(func() { dataDir = saved })
	fakeNode(t, "bch2", &bch2RPC, func(string) {})
	starts := &atomic.Int32{}
	start := pipedStart("bch2", "eof", t.TempDir(), starts)
	supervise(t, "bch2", start)
	if err := start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	stopNodes()
	time.Sleep(time.Second)
	if starts.Load() != 1 {
		t.Fatalf("SUPERVISE-NODES: a node asked to stop was started again (%d starts)", starts.Load())
	}
}
