package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

var errStopping = errors.New("stopping")

// child is one supervised program. Start runs it; if it exits on its own it is started again after
// a delay that doubles to a minute; Stop sends SIGTERM, waits up to grace, then SIGKILL.
type child struct {
	name     string
	path     string
	args     []string
	onceArgs []string // added at the first start only (forge-solo run --reindex)
	env      []string
	dir      string
	grace    time.Duration
	log      *rotatingLog

	mu       sync.Mutex
	cmd      *exec.Cmd
	exited   chan struct{} // closed once the current process has been reaped
	waitErr  error         // how it exited
	started  time.Time
	spawns   int // starts so far
	stopping bool
	stopCh   chan struct{} // closed by Stop: wakes a restart delay
	done     chan struct{} // closed when supervise returns
}

// Start runs the program, then keeps it running until Stop. The first start is synchronous, so a
// missing or broken binary is reported to the caller.
func (c *child) Start() error {
	c.mu.Lock()
	c.stopCh = make(chan struct{})
	c.done = make(chan struct{})
	c.mu.Unlock()
	if err := c.spawn(); err != nil {
		close(c.done)
		return err
	}
	go c.supervise()
	return nil
}

func (c *child) spawn() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopping {
		return errStopping
	}
	args := c.args
	if c.spawns == 0 {
		args = append(append([]string(nil), c.args...), c.onceArgs...)
	}
	cmd := exec.Command(c.path, args...)
	cmd.Env = c.env
	cmd.Dir = c.dir
	cmd.Stdout = c.log
	cmd.Stderr = c.log
	// Its own process group, so a Ctrl-C in the terminal reaches only this launcher, which stops
	// the programs in order. Pdeathsig: if the launcher is killed outright, each program still
	// gets SIGTERM and shuts down cleanly instead of running on unsupervised. Not under systemd:
	// there KillMode=mixed SIGKILLs them as soon as the launcher has exited (see unitFile).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	if err := cmd.Start(); err != nil {
		return err
	}
	c.spawns++
	exited := make(chan struct{})
	c.cmd, c.exited, c.started, c.waitErr = cmd, exited, time.Now(), nil
	go func() {
		err := cmd.Wait()
		c.mu.Lock()
		c.waitErr = err
		c.mu.Unlock()
		close(exited)
	}()
	return nil
}

func (c *child) supervise() {
	defer close(c.done)
	delay := 2 * time.Second
	for {
		c.mu.Lock()
		exited, stopCh := c.exited, c.stopCh
		c.mu.Unlock()
		<-exited
		c.mu.Lock()
		stopping, err, ran := c.stopping, c.waitErr, time.Since(c.started)
		c.mu.Unlock()
		if stopping {
			return
		}
		if err == nil {
			err = errors.New("exit status 0")
		}
		logf("%s stopped unexpectedly (%v). Its last lines, from %s:", c.name, err, c.log.path)
		for _, l := range c.log.Tail(15) {
			fmt.Fprintln(logOut, "    "+l)
		}
		if ran > 10*time.Minute {
			delay = 2 * time.Second
		}
		logf("starting %s again in %s", c.name, delay)
		select {
		case <-stopCh:
			return
		case <-time.After(delay):
		}
		if delay *= 2; delay > time.Minute {
			delay = time.Minute
		}
		for {
			err := c.spawn()
			if err == nil {
				break
			}
			if errors.Is(err, errStopping) {
				return
			}
			logf("%s could not be started: %v; trying again in %s", c.name, err, delay)
			select {
			case <-stopCh:
				return
			case <-time.After(delay):
			}
		}
	}
}

// Stop ends the program for good: SIGTERM, then SIGKILL if it is still running after grace.
func (c *child) Stop() {
	c.mu.Lock()
	if c.done == nil {
		c.mu.Unlock()
		return
	}
	if !c.stopping {
		c.stopping = true
		close(c.stopCh)
	}
	cmd, exited, done := c.cmd, c.exited, c.done
	c.mu.Unlock()
	if cmd != nil {
		select {
		case <-exited:
		default:
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-exited:
			case <-time.After(c.grace):
				logf("%s did not stop within %s; killing it", c.name, c.grace)
				_ = cmd.Process.Kill()
				<-exited
			}
		}
	}
	<-done
}

// Kill ends the program at once (a second Ctrl-C).
func (c *child) Kill() {
	c.mu.Lock()
	c.stopping = true
	cmd := c.cmd
	c.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// rotatingLog is a program's output file. Past max bytes it is renamed to <name>.1, replacing the
// previous one, so the logs never hold more than about twice max.
type rotatingLog struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

func newRotatingLog(path string, max int64) *rotatingLog { return &rotatingLog{path: path, max: max} }

func (l *rotatingLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil && l.size+int64(len(p)) > l.max {
		_ = l.f.Close()
		l.f = nil
		_ = os.Rename(l.path, l.path+".1")
	}
	if l.f == nil {
		if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
			return 0, err
		}
		f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return 0, err
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			return 0, err
		}
		l.f, l.size = f, st.Size()
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

// Tail returns up to n of the last lines written.
func (l *rotatingLog) Tail(n int) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	const window = 8 << 10
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	off := st.Size() - window
	if off < 0 {
		off = 0
	}
	b, err := io.ReadAll(io.NewSectionReader(f, off, st.Size()-off))
	if err != nil {
		return nil
	}
	b = bytes.TrimRight(b, "\n")
	lines := strings.Split(string(b), "\n")
	if off > 0 && len(lines) > 1 {
		lines = lines[1:] // the first line was cut by the window
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i, s := range lines {
		if len(s) > 300 {
			lines[i] = s[:300] + "…"
		}
	}
	return lines
}

func (l *rotatingLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// logOut is where the launcher reports: the terminal, or the journal under systemd.
var logOut io.Writer = os.Stdout

var logMu sync.Mutex

func logf(format string, a ...interface{}) {
	logMu.Lock()
	defer logMu.Unlock()
	fmt.Fprintf(logOut, "%s  %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, a...))
}
