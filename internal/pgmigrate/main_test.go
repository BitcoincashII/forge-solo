//go:build sqlite

package pgmigrate

import (
	"bufio"
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/stats"
)

// A test plays another program (a migrator killed half way, the api holding the database) with a
// copy of this test binary, started with childEnv naming the part and childDBEnv the database. A
// part is a func registered in childParts; it talks to the test in lines on stdin and stdout.
const (
	childEnv   = "FORGE_PGMIGRATE_TEST_CHILD"
	childDBEnv = "FORGE_PGMIGRATE_TEST_DB"
)

var childParts = map[string]func(db string) int{}

// The tests, and the programs they start, run five hours behind UTC, as a PC in the Americas does,
// so a time converted in the local zone instead of UTC reads five hours off.
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

// freshDB is a database this build's stats.InitDB has just made, as a new install has it.
func freshDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "forgesolo.db")
	if err := stats.InitDB(path); err != nil {
		t.Fatal(err)
	}
	stats.CloseDB()
	db, err := sql.Open("sqlite", stats.SQLiteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

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

// expect fails the test, tagged code, unless the child's next line within d starts with want.
func (c *child) expect(t *testing.T, want string, d time.Duration, code string) string {
	t.Helper()
	select {
	case line, ok := <-c.lines:
		if !ok {
			t.Fatalf("%s: the %s child ended without a word; its log:\n%s", code, c.name, c.stderr.String())
		}
		if !strings.HasPrefix(line, want) {
			t.Fatalf("%s: the %s child said %q, want %q; its log:\n%s", code, c.name, line, want, c.stderr.String())
		}
		return line
	case <-time.After(d):
		t.Fatalf("%s: the %s child said nothing for %v; its log:\n%s", code, c.name, d, c.stderr.String())
	}
	return ""
}
