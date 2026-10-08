package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestGatewayChildProcess is the gateway program as a child of the tests below: with GW_CHILD=1 it
// runs main with the arguments in GW_CHILD_ARGS, one per line.
func TestGatewayChildProcess(t *testing.T) {
	if os.Getenv("GW_CHILD") != "1" {
		return
	}
	os.Args = append([]string{"forge-gateway"}, strings.Split(os.Getenv("GW_CHILD_ARGS"), "\n")...)
	main()
	os.Exit(0)
}

// child is the gateway program started as a child process.
type child struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr bytes.Buffer
	lines  chan string // its stdout, line by line
	done   chan error
}

func startChild(t *testing.T, env []string, args ...string) *child {
	t.Helper()
	c := &child{lines: make(chan string, 1000), done: make(chan error, 1)}
	c.cmd = exec.Command(os.Args[0], "-test.run=^TestGatewayChildProcess$")
	c.cmd.Env = append(append(os.Environ(), "GW_CHILD=1", "GW_CHILD_ARGS="+strings.Join(args, "\n"), "SETTINGS_PASSWORD="), env...)
	c.cmd.Stderr = &c.stderr
	out, err := c.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if c.stdin, err = c.cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	if err := c.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if c.cmd.ProcessState == nil {
			c.cmd.Process.Kill()
		}
	})
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			select {
			case c.lines <- sc.Text():
			default:
			}
		}
		close(c.lines)
		c.done <- c.cmd.Wait()
	}()
	return c
}

// waitLine waits for a line of the child's output holding s, and returns the lines read.
func (c *child) waitLine(t *testing.T, s string, within time.Duration) []string {
	t.Helper()
	var seen []string
	end := time.After(within)
	for {
		select {
		case l, ok := <-c.lines:
			if !ok {
				t.Fatalf("the child ended before it said %q; it said:\n%s\nstderr: %s", s, strings.Join(seen, "\n"), c.stderr.String())
			}
			seen = append(seen, l)
			if strings.Contains(l, s) {
				return seen
			}
		case <-end:
			t.Fatalf("the child did not say %q within %s; it said:\n%s", s, within, strings.Join(seen, "\n"))
		}
	}
}

// exit waits for the child to end and returns its exit code and the rest of its output.
func (c *child) exit(t *testing.T, within time.Duration) (int, []string) {
	t.Helper()
	var rest []string
	end := time.After(within)
	for {
		select {
		case l, ok := <-c.lines:
			if ok {
				rest = append(rest, l)
				continue
			}
			c.lines = nil
		case err := <-c.done:
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return ee.ExitCode(), rest
			}
			if err != nil {
				t.Fatal(err)
			}
			return 0, rest
		case <-end:
			return -1, rest
		}
	}
}

// exitCode gives a config or SETTINGS_PASSWORD problem 3, a listen address 4, the rest 1.
func TestExitCodes(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{&exitError{exitConfig, errors.New("x")}, 3},
		{&exitError{exitPort, errors.New("x")}, 4},
		{fmt.Errorf("wrapped: %w", &exitError{exitPort, errors.New("x")}), 4},
		{errors.New("anything else"), 1},
		{errUnauthorized, 1},
	}
	for _, k := range cases {
		if got := exitCode(k.err); got != k.want {
			t.Errorf("GW-EXIT: exitCode(%v) = %d, want %d", k.err, got, k.want)
		}
	}
	if e := (&exitError{exitConfig, errUnauthorized}); e.Error() != errUnauthorized.Error() || !errors.Is(e, errUnauthorized) {
		t.Errorf("GW-EXIT-WRAP: %q", e.Error())
	}
}

// As a program: a config it cannot use ends it with 3, a stratum or status port another program
// holds with 4, and stderr's last line says why.
func TestExitCodesAsAProgram(t *testing.T) {
	bad := writeConfig(t, `{"x": 1}`)
	c := startChild(t, nil, "-config", bad)
	if code, _ := c.exit(t, 30*time.Second); code != 3 || !strings.Contains(c.stderr.String(), `unknown field "x"`) {
		t.Errorf("GW-EXIT-CONFIG: exit %d, stderr %q", code, c.stderr.String())
	}

	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	n := newFakeNode(t, "u", "p")
	for _, which := range []string{"stratum", "status"} {
		cfg := nodeConfigAt(n, held.Addr().String(), freeAddr(t))
		if which == "status" {
			cfg = nodeConfigAt(n, freeAddr(t), held.Addr().String())
		}
		c := startChild(t, nil, "-config", writeConfig(t, cfg))
		code, _ := c.exit(t, 30*time.Second)
		if code != 4 || !strings.HasPrefix(c.stderr.String(), "forge-gateway: "+which+" "+held.Addr().String()+": ") {
			t.Errorf("GW-EXIT-PORT %s: exit %d, stderr %q", which, code, c.stderr.String())
		}
	}
}

// Forge Gateway's Windows tray app cannot signal the gateway, so it closes the gateway's stdin to
// stop it: the gateway must then stop as on Ctrl+C (closing the miners, sending the queued shares),
// not wait to be killed. (cmd/stratum's two tests, for the gateway's copy.)
func TestStopOnEOFAsksForShutdown(t *testing.T) {
	r, w := io.Pipe()
	stop := make(chan os.Signal, 1)
	go stopOnEOF(r, stop)
	select {
	case s := <-stop:
		t.Fatalf("GW-EOF: asked to stop (%v) before stdin closed", s)
	case <-time.After(100 * time.Millisecond):
	}
	w.Close()
	select {
	case s := <-stop:
		if s != syscall.SIGTERM {
			t.Fatalf("GW-EOF: got %v, want SIGTERM", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("GW-EOF: closing stdin did not ask for shutdown")
	}
}

// A shutdown already asked for (a real signal first) must not be blocked on.
func TestStopOnEOFDoesNotBlockOnAPendingStop(t *testing.T) {
	r, w := io.Pipe()
	stop := make(chan os.Signal, 1)
	stop <- syscall.SIGINT
	done := make(chan struct{})
	go func() { stopOnEOF(r, stop); close(done) }()
	w.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("GW-EOF-PENDING: stopOnEOF blocked on a full stop channel")
	}
}

// With FORGE_STOP_ON_STDIN_EOF=1, as the tray app starts it, closing the gateway's stdin stops it
// cleanly: exit 0, with the stop logged.
func TestClosingStdinStopsTheGateway(t *testing.T) {
	n := newFakeNode(t, "u", "p")
	c := startChild(t, []string{"FORGE_STOP_ON_STDIN_EOF=1"}, "-config", writeConfig(t, nodeConfig(t, n)))
	c.waitLine(t, "ready: point your miners here", 30*time.Second)
	c.stdin.Close()
	code, rest := c.exit(t, 10*time.Second)
	if code != 0 {
		t.Fatalf("GW-EOF-RUN: exit %d within 10s after stdin closed (-1: still running); stderr %q", code, c.stderr.String())
	}
	if !strings.Contains(strings.Join(rest, "\n"), "stopping: closing the miners' connections, then sending the pool the shares still queued") {
		t.Fatalf("GW-EOF-RUN: the stop was not logged:\n%s", strings.Join(rest, "\n"))
	}
}
