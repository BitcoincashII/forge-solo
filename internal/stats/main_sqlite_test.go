//go:build sqlite

package stats

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// The api and the stratum are two programs on one database file. A test plays the other program
// with a copy of this test binary, started with childEnv naming the part it plays and childDBEnv
// the database. Each part is a func registered in childParts by the test file that uses it; it
// talks to the test in lines on stdin and stdout, and its result is the exit code.
const (
	childEnv   = "FORGE_STATS_TEST_CHILD"
	childDBEnv = "FORGE_STATS_TEST_DB"
)

var childParts = map[string]func(db string) int{}

// The SQLite build stores every time in UTC. Its tests, and the other programs they start, run
// five hours behind UTC, as a PC in the Americas does, so a time stored in the local zone reads
// five hours off.
func TestMain(m *testing.M) {
	time.Local = time.FixedZone("UTC-5", -5*60*60)
	if part := os.Getenv(childEnv); part != "" {
		run, ok := childParts[part]
		if !ok {
			fmt.Fprintf(os.Stderr, "no child part %q\n", part)
			os.Exit(2)
		}
		os.Exit(run(os.Getenv(childDBEnv)))
	}
	os.Exit(m.Run())
}

// waitForStdin returns once the test closes the child's stdin: the test's signal to go on.
func waitForStdin() { io.Copy(io.Discard, os.Stdin) }

// child is another program on the test's database.
type child struct {
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan string
	stderr lockedBuffer
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// startChild starts a copy of this test binary playing part on the database at dbPath. It is
// stopped, if it is still running, when the test ends.
func startChild(t *testing.T, part, dbPath string) *child {
	t.Helper()
	c := &child{name: part, lines: make(chan string, 64)}
	c.cmd = exec.Command(os.Args[0], "-test.run=^$")
	c.cmd.Env = append(os.Environ(), childEnv+"="+part, childDBEnv+"="+dbPath)
	c.cmd.Stderr = &c.stderr
	var err error
	if c.stdin, err = c.cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	stdout, err := c.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			c.lines <- sc.Text()
		}
		close(c.lines)
	}()
	t.Cleanup(func() {
		c.stdin.Close()
		drained := make(chan struct{})
		go func() {
			for range c.lines {
			}
			close(drained)
		}()
		select {
		case <-drained:
		case <-time.After(10 * time.Second):
			c.cmd.Process.Kill()
			<-drained
		}
		c.cmd.Wait()
	})
	return c
}

// next is the child's next line, or a test failure tagged code when none comes within d.
func (c *child) next(t *testing.T, d time.Duration, code string) string {
	t.Helper()
	select {
	case line, ok := <-c.lines:
		if !ok {
			t.Fatalf("%s: the %s child ended without a word; its log:\n%s", code, c.name, c.stderr.String())
		}
		return line
	case <-time.After(d):
		t.Fatalf("%s: the %s child said nothing for %v; its log:\n%s", code, c.name, d, c.stderr.String())
	}
	return ""
}

// expect fails the test, tagged code, unless the child's next line within d starts with want.
func (c *child) expect(t *testing.T, want string, d time.Duration, code string) string {
	t.Helper()
	line := c.next(t, d, code)
	if !strings.HasPrefix(line, want) {
		t.Fatalf("%s: the %s child said %q, want %q; its log:\n%s", code, c.name, line, want, c.stderr.String())
	}
	return line
}

// release closes the child's stdin: the signal it waits for.
func (c *child) release() { c.stdin.Close() }

// say sends the child a line.
func (c *child) say(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(c.stdin, line+"\n"); err != nil {
		t.Fatalf("telling the %s child %q: %v", c.name, line, err)
	}
}
