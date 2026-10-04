//go:build sqlite && unix

package pgmigrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Server is the private PostgreSQL server run starts on the old data.
type Server struct {
	cmd     *exec.Cmd
	dir     string // its socket and its hba file
	pgdata  string
	port    string
	log     *tail
	exited  chan struct{}
	stop    sync.Once
	stopErr error
}

// serverArgs are the private server's flags: no TCP, a socket and an hba file of its own (trust for
// local connections, which only this container makes), nothing preloaded (TimescaleDB is not in the
// image, and the shares it held are never read), little memory, no autovacuum, every transaction
// read-only, and one syncfs instead of an fsync per file before the crash recovery a hard-stopped
// cluster needs.
func serverArgs(pgdata, dir string) []string {
	return []string{"-D", pgdata,
		"-c", "listen_addresses=",
		"-c", "unix_socket_directories=" + dir,
		"-c", "hba_file=" + filepath.Join(dir, "pg_hba.conf"),
		"-c", "shared_preload_libraries=",
		"-c", "shared_buffers=16MB",
		"-c", "max_connections=10",
		"-c", "max_parallel_workers=0",
		"-c", "autovacuum=off",
		"-c", "default_transaction_read_only=on",
		"-c", "jit=off",
		"-c", "huge_pages=off",
		"-c", "ssl=off",
		"-c", "logging_collector=off",
		"-c", "archive_mode=off",
		"-c", "recovery_init_sync_method=syncfs",
	}
}

// StartServer starts postgres on pgdata as the folder's owner and waits, at most ReadyWait, until it
// says it is ready. The Server it returns also when the start fails holds the server's log.
func StartServer(ctx context.Context, pgdata string, logf func(string, ...any)) (*Server, error) {
	bin, err := exec.LookPath("postgres")
	if err != nil {
		return nil, newErr(CodeSource, "PostgreSQL is not in this image", err)
	}
	fi, err := os.Stat(pgdata)
	if err != nil {
		return nil, newErr(CodeSource, "the old data cannot be read", err)
	}
	st, _ := fi.Sys().(*syscall.Stat_t)
	if st == nil {
		return nil, newErr(CodeOther, "the old data's owner cannot be read", nil)
	}
	dir, err := os.MkdirTemp("", "forge-migrate-")
	if err != nil {
		return nil, newErr(CodeWrite, "a folder for the old data's server could not be made", err)
	}
	s := &Server{dir: dir, pgdata: pgdata, log: &tail{}, exited: make(chan struct{})}
	hba := filepath.Join(dir, "pg_hba.conf")
	err = os.WriteFile(hba, []byte("local all all trust\n"), 0o600)
	root := os.Geteuid() == 0
	if err == nil && root {
		if err = os.Chown(dir, int(st.Uid), int(st.Gid)); err == nil {
			err = os.Chown(hba, int(st.Uid), int(st.Gid))
		}
	}
	if err != nil {
		os.RemoveAll(dir)
		return nil, newErr(CodeWrite, "a folder for the old data's server could not be made", err)
	}
	s.cmd = exec.Command(bin, serverArgs(pgdata, dir)...)
	s.cmd.Stdout, s.cmd.Stderr = s.log, s.log
	if root && st.Uid != 0 {
		// PostgreSQL refuses to run as root; it runs as the owner of its data, as it always has.
		s.cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: st.Uid, Gid: st.Gid}}
	}
	if err := s.cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return nil, newErr(CodeSource, "the old data's server could not be started", err)
	}
	go func() {
		s.cmd.Wait()
		close(s.exited)
	}()
	logf("the old data's server started (pid %d); waiting until it is ready", s.cmd.Process.Pid)
	deadline := time.After(ReadyWait)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if port, ok := s.ready(); ok {
			s.port = port
			return s, nil
		}
		select {
		case <-s.exited:
			s.Stop()
			return s, newErr(CodeSource, "the old data's server stopped before it was ready", errors.New(s.cmd.ProcessState.String()))
		case <-ctx.Done():
			s.Stop()
			return s, ctx.Err()
		case <-deadline:
			s.Stop()
			return s, newErr(CodeSource, "the old data's server was not ready within "+ReadyWait.String(), nil)
		case <-tick.C:
		}
	}
}

// ready reads the server's postmaster.pid: the server's own pid on its first line and "ready" on
// its eighth. It returns the port, which names the socket.
func (s *Server) ready() (string, bool) {
	b, err := os.ReadFile(filepath.Join(s.pgdata, "postmaster.pid"))
	if err != nil {
		return "", false
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) < 8 || strings.TrimSpace(lines[0]) != strconv.Itoa(s.cmd.Process.Pid) || strings.TrimSpace(lines[7]) != "ready" {
		return "", false
	}
	return strings.TrimSpace(lines[3]), true
}

// DSN connects to the server through its socket, as forge: the user 1.0.12 made the cluster with.
func (s *Server) DSN() string {
	return fmt.Sprintf("host='%s' port=%s user=forge dbname=forgesolo sslmode=disable connect_timeout=30", s.dir, s.port)
}

// Stop shuts the server down so that the old data is left cleanly shut down: SIGINT, PostgreSQL's
// fast shutdown with a checkpoint; after StopWait SIGQUIT, an immediate shutdown that leaves crash
// recovery for the next start (and commit then refuses); after another StopWait, SIGKILL. It waits
// for the server to end and removes its folder. It returns an error unless SIGINT did it.
func (s *Server) Stop() error {
	if s == nil {
		return nil
	}
	s.stop.Do(func() {
		select {
		case <-s.exited:
		default:
			s.cmd.Process.Signal(syscall.SIGINT)
			select {
			case <-s.exited:
			case <-time.After(StopWait):
				s.stopErr = fmt.Errorf("the server did not stop within %v of SIGINT", StopWait)
				s.cmd.Process.Signal(syscall.SIGQUIT)
				select {
				case <-s.exited:
				case <-time.After(StopWait):
					s.cmd.Process.Kill()
					<-s.exited
				}
			}
		}
		os.RemoveAll(s.dir)
	})
	return s.stopErr
}

// LogTail is the last n lines the server logged.
func (s *Server) LogTail(n int) string {
	if s == nil || s.log == nil {
		return ""
	}
	return s.log.last(n)
}

// tail keeps the end of what a program writes.
type tail struct {
	mu sync.Mutex
	b  []byte
}

const tailKeep = 64 << 10

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > tailKeep {
		t.b = t.b[len(t.b)-tailKeep:]
	}
	return len(p), nil
}

func (t *tail) last(n int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimRight(string(t.b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
