package dblock

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Another process is a copy of this test binary, started with childEnv naming what it does with
// the lock file named by pathEnv. It reports in lines on stdout and holds its lock until the test
// closes its stdin.
const (
	childEnv = "FORGE_DBLOCK_TEST_CHILD"
	pathEnv  = "FORGE_DBLOCK_TEST_PATH"
)

func TestMain(m *testing.M) {
	if part := os.Getenv(childEnv); part != "" {
		os.Exit(runChild(part, os.Getenv(pathEnv)))
	}
	os.Exit(m.Run())
}

func runChild(part, path string) int {
	var l *Lock
	var err error
	switch part {
	case "shared":
		l, err = Shared(path, 0)
	case "exclusive":
		l, err = TryExclusive(path)
	default:
		fmt.Println("error: no part", part)
		return 2
	}
	switch {
	case errors.Is(err, ErrBusy):
		fmt.Println("busy")
		return 0
	case err != nil:
		fmt.Println("error:", err)
		return 1
	}
	fmt.Println("held")
	io.Copy(io.Discard, os.Stdin)
	if err := l.Release(); err != nil {
		fmt.Println("error:", err)
		return 1
	}
	return 0
}

type proc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	lines chan string
}

// start runs a copy of this test binary that takes the lock at path (part: shared or exclusive)
// and holds it until release, or until it is killed.
func start(t *testing.T, part, path string) *proc {
	t.Helper()
	p := &proc{lines: make(chan string, 8)}
	p.cmd = exec.Command(os.Args[0], "-test.run=^$")
	p.cmd.Env = append(os.Environ(), childEnv+"="+part, pathEnv+"="+path)
	p.cmd.Stderr = os.Stderr
	var err error
	if p.stdin, err = p.cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	out, err := p.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	drained := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			p.lines <- sc.Text()
		}
		close(p.lines)
		close(drained)
	}()
	t.Cleanup(func() {
		p.stdin.Close()
		select {
		case <-drained:
		case <-time.After(10 * time.Second):
			p.cmd.Process.Kill()
			<-drained
		}
		p.cmd.Wait()
	})
	return p
}

// says is the process's first line: held, busy or an error.
func (p *proc) says(t *testing.T) string {
	t.Helper()
	select {
	case line := <-p.lines:
		return line
	case <-time.After(30 * time.Second):
		t.Fatal("the other process said nothing for 30 s")
	}
	return ""
}

// release makes the process let its lock go, and waits until it has.
func (p *proc) release(t *testing.T) {
	t.Helper()
	p.stdin.Close()
	for range p.lines {
	}
	if err := p.cmd.Wait(); err != nil {
		t.Fatalf("the other process: %v", err)
	}
}

// otherSays is what a process trying part on the lock at path says.
func otherSays(t *testing.T, part, path string) string {
	t.Helper()
	p := start(t, part, path)
	got := p.says(t)
	p.release(t)
	return got
}

func lockPath(t *testing.T) string {
	return Path(filepath.Join(t.TempDir(), "forgesolo.db"))
}

// The api and the stratum both hold the lock, and while either does, nobody takes it exclusively.
func TestSharedLocksAreHeldTogether(t *testing.T) {
	path := lockPath(t)
	mine, err := Shared(path, 0)
	if err != nil {
		t.Fatalf("DBLOCK-SHARED: %v", err)
	}
	other := start(t, "shared", path)
	if got := other.says(t); got != "held" {
		t.Fatalf("DBLOCK-SHARED: another process could not share the lock: it says %q", got)
	}
	again, err := Shared(path, 0)
	if err != nil {
		t.Fatalf("DBLOCK-SHARED-SAME: a second shared lock in one process: %v", err)
	}
	if got := otherSays(t, "exclusive", path); got != "busy" {
		t.Fatalf("DBLOCK-EXCL-WHILE-SHARED: a process took the lock exclusively while two held it shared: it says %q", got)
	}
	mine.Release()
	again.Release()
	if got := otherSays(t, "exclusive", path); got != "busy" {
		t.Fatalf("DBLOCK-EXCL-WHILE-SHARED: a process took the lock exclusively while another held it shared: it says %q", got)
	}
	other.release(t)
	if got := otherSays(t, "exclusive", path); got != "held" {
		t.Fatalf("DBLOCK-RELEASED: once every holder let go, the exclusive lock was still refused: %q", got)
	}
}

// While the lock is held exclusively, nobody gets it, and a Shared that waits gets it once the
// holder lets go.
func TestExclusiveKeepsOthersOut(t *testing.T) {
	path := lockPath(t)
	mover := start(t, "exclusive", path)
	if got := mover.says(t); got != "held" {
		t.Fatalf("DBLOCK-EXCL: %q", got)
	}
	if l, err := Shared(path, 0); !errors.Is(err, ErrBusy) {
		l.Release()
		t.Fatalf("DBLOCK-SHARED-WHILE-EXCL: a shared lock was taken while another process held it exclusively (%v)", err)
	}
	if l, err := TryExclusive(path); !errors.Is(err, ErrBusy) {
		l.Release()
		t.Fatalf("DBLOCK-EXCL-WHILE-EXCL: two processes held the lock exclusively (%v)", err)
	}
	began := time.Now()
	let := make(chan struct{})
	go func() {
		time.Sleep(500 * time.Millisecond)
		mover.stdin.Close()
		close(let)
	}()
	l, err := Shared(path, 10*time.Second)
	<-let
	if err != nil {
		t.Fatalf("DBLOCK-WAIT: a waiting shared lock was not given once the holder let go: %v", err)
	}
	defer l.Release()
	if d := time.Since(began); d < 400*time.Millisecond {
		t.Fatalf("DBLOCK-WAIT: the shared lock was given after %v, while the other process still held it", d)
	}
	if l2, err := Shared(path, 300*time.Millisecond); err != nil {
		t.Fatalf("DBLOCK-WAIT: %v", err)
	} else {
		l2.Release()
	}
}

// A Shared that waits gives up once its time is over.
func TestSharedWaitEnds(t *testing.T) {
	path := lockPath(t)
	mover := start(t, "exclusive", path)
	if got := mover.says(t); got != "held" {
		t.Fatalf("DBLOCK-EXCL: %q", got)
	}
	began := time.Now()
	l, err := Shared(path, 300*time.Millisecond)
	if !errors.Is(err, ErrBusy) {
		l.Release()
		t.Fatalf("DBLOCK-WAIT-ENDS: %v", err)
	}
	if d := time.Since(began); d < 250*time.Millisecond || d > 5*time.Second {
		t.Fatalf("DBLOCK-WAIT-ENDS: a 300 ms wait took %v", d)
	}
}

// Two opens in one process are two holders, as two processes are. The program that moves the
// database relies on this to see its own earlier open.
func TestOneProcessTwoHolders(t *testing.T) {
	path := lockPath(t)
	s, err := Shared(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if l, err := TryExclusive(path); !errors.Is(err, ErrBusy) {
		l.Release()
		t.Fatalf("DBLOCK-SAME-PROCESS: the exclusive lock was taken over this process's own shared lock (%v)", err)
	}
	s.Release()
	x, err := TryExclusive(path)
	if err != nil {
		t.Fatalf("DBLOCK-SAME-PROCESS: %v", err)
	}
	if l, err := Shared(path, 0); !errors.Is(err, ErrBusy) {
		l.Release()
		t.Fatalf("DBLOCK-SAME-PROCESS: a shared lock was taken over this process's own exclusive lock (%v)", err)
	}
	x.Release()
	if err := x.Release(); err != nil {
		t.Fatalf("DBLOCK-RELEASE-TWICE: %v", err)
	}
}

// A process that ends without letting go, as a crash does, leaves no lock behind.
func TestLockEndsWithItsProcess(t *testing.T) {
	path := lockPath(t)
	p := start(t, "shared", path)
	if got := p.says(t); got != "held" {
		t.Fatalf("DBLOCK-CRASH: %q", got)
	}
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	// Windows ends the locks of a process that ended a moment after it has.
	deadline := time.Now().Add(5 * time.Second)
	for {
		l, err := TryExclusive(path)
		if err == nil {
			l.Release()
			return
		}
		if !errors.Is(err, ErrBusy) || time.Now().After(deadline) {
			t.Fatalf("DBLOCK-CRASH: the lock of a process that was killed is still held: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The lock file is made 0600 and stays after the lock ends, and a lock file this account can
// only read still locks.
func TestLockFileStays(t *testing.T) {
	path := lockPath(t)
	l, err := Shared(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("DBLOCK-FILE: the lock file is gone after the lock ended: %v", err)
	}
	if runtime.GOOS != "windows" {
		if m := fi.Mode().Perm(); m != 0o600 {
			t.Fatalf("DBLOCK-FILE: the lock file is %o, want 600", m)
		}
		if err := os.Chmod(path, 0o400); err != nil {
			t.Fatal(err)
		}
	}
	x, err := TryExclusive(path)
	if err != nil {
		t.Fatalf("DBLOCK-FILE-READONLY: %v", err)
	}
	if got := otherSays(t, "shared", path); got != "busy" {
		t.Fatalf("DBLOCK-FILE-READONLY: a read-only lock file did not lock: another process says %q", got)
	}
	x.Release()
}
