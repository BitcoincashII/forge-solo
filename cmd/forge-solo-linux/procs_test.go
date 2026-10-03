package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func shChild(t *testing.T, script string, grace time.Duration) (*child, string) {
	t.Helper()
	dir := t.TempDir()
	return &child{name: "test", path: "/bin/sh", args: []string{"-c", script}, dir: dir, grace: grace,
		env: os.Environ(), log: newRotatingLog(filepath.Join(dir, "test.log"), 1<<20)}, dir
}

func countLines(t *testing.T, p string) int {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "\n")
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return cond()
}

// captureLog collects what the launcher reports for the rest of the test.
func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	b := &syncBuffer{}
	old := logOut
	logOut = b
	t.Cleanup(func() { logOut = old })
	return b
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// A program that exits on its own is started again, and never again once stopped.
func TestChildRestartsUntilStopped(t *testing.T) {
	out := captureLog(t)
	c, dir := shChild(t, `echo run >> starts; exit 3`, time.Second)
	starts := filepath.Join(dir, "starts")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 5*time.Second, func() bool { return countLines(t, starts) >= 2 }) {
		t.Fatalf("not restarted: %d starts", countLines(t, starts))
	}
	if !strings.Contains(out.String(), "test stopped unexpectedly (exit status 3)") {
		t.Fatalf("the exit was not reported:\n%s", out)
	}
	c.Stop()
	n := countLines(t, starts)
	time.Sleep(4500 * time.Millisecond) // past the next restart delay (4 s)
	if m := countLines(t, starts); m != n {
		t.Fatalf("started again after Stop: %d then %d", n, m)
	}
}

// A program that exits because it was stopped is not reported as a failure.
func TestChildStopIsNotReportedAsACrash(t *testing.T) {
	out := captureLog(t)
	c, dir := shChild(t, `trap 'exit 0' TERM; echo up > up; while :; do sleep 0.05; done`, 5*time.Second)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 3*time.Second, func() bool { _, err := os.Stat(filepath.Join(dir, "up")); return err == nil }) {
		t.Fatal("did not start")
	}
	c.Stop()
	time.Sleep(200 * time.Millisecond)
	if s := out.String(); strings.Contains(s, "unexpectedly") || strings.Contains(s, "again") {
		t.Fatalf("a requested stop was reported as a crash:\n%s", s)
	}
}

// Stop asks with SIGTERM and returns once the program has exited.
func TestChildStopSendsSIGTERM(t *testing.T) {
	c, dir := shChild(t, `trap 'echo term > got; exit 0' TERM; echo up > up; while :; do sleep 0.05; done`, 5*time.Second)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 3*time.Second, func() bool { _, err := os.Stat(filepath.Join(dir, "up")); return err == nil }) {
		t.Fatal("did not start")
	}
	t0 := time.Now()
	c.Stop()
	if d := time.Since(t0); d > 3*time.Second {
		t.Fatalf("Stop took %s", d)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "got")); string(b) != "term\n" {
		t.Fatalf("SIGTERM not delivered: %q", b)
	}
}

// A program that ignores SIGTERM is killed once its grace runs out.
func TestChildStopKillsAfterGrace(t *testing.T) {
	c, dir := shChild(t, `trap '' TERM; echo $$ > pid; while :; do sleep 0.05; done`, 700*time.Millisecond)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 3*time.Second, func() bool { _, err := os.Stat(filepath.Join(dir, "pid")); return err == nil }) {
		t.Fatal("did not start")
	}
	t0 := time.Now()
	c.Stop()
	d := time.Since(t0)
	if d < 600*time.Millisecond || d > 3*time.Second {
		t.Fatalf("Stop took %s, want about the 700ms grace", d)
	}
	c.mu.Lock()
	ps := c.cmd.ProcessState
	c.mu.Unlock()
	if ps == nil || ps.Exited() {
		t.Fatalf("process state %v, want killed by a signal", ps)
	}
}

func TestChildStartReportsAMissingProgram(t *testing.T) {
	c := &child{name: "x", path: "/nonexistent/prog", log: newRotatingLog(filepath.Join(t.TempDir(), "x.log"), 1<<20)}
	if err := c.Start(); err == nil {
		t.Fatal("no error for a missing program")
	}
	c.Stop() // must not hang
}

func TestRotatingLogRotatesAndTails(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.log")
	l := newRotatingLog(p, 100)
	for i := 0; i < 30; i++ {
		if _, err := l.Write([]byte("line " + strings.Repeat("x", 5) + string(rune('a'+i%26)) + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	defer l.Close()
	st, err := os.Stat(p)
	if err != nil || st.Size() > 100 {
		t.Fatalf("current log %v bytes (%v), want at most 100", st.Size(), err)
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatalf("no rotated log: %v", err)
	}
	tail := l.Tail(2)
	if len(tail) != 2 || tail[1] != "line xxxxxd" {
		t.Fatalf("tail %q", tail)
	}
}

// First-start arguments (-reindex) go to the first start only: a restart in the same run must not
// begin the rebuild again.
func TestOnceArgsGoToTheFirstStartOnly(t *testing.T) {
	captureLog(t)
	c, dir := shChild(t, `echo "start:$*" >> starts; exit 3`, time.Second)
	c.args = append(c.args, "sh") // $0 for sh -c; what follows is $*
	c.onceArgs = []string{"-reindex"}
	starts := filepath.Join(dir, "starts")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 10*time.Second, func() bool { return countLines(t, starts) >= 3 }) {
		t.Fatalf("not restarted: %d starts", countLines(t, starts))
	}
	c.Stop()
	b, err := os.ReadFile(starts)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if lines[0] != "start:-reindex" {
		t.Errorf("REINDEX-FIRST: the first start got %q, want start:-reindex", lines[0])
	}
	for i, l := range lines[1:] {
		if l != "start:" {
			t.Errorf("REINDEX-ONCE: start %d got %q, want no first-start arguments", i+2, l)
		}
	}
}
