//go:build !windows

package main

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// alive reports whether pid is a running process (not one that has exited and waits to be reaped).
func alive(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	f := strings.Fields(string(b)[strings.LastIndexByte(string(b), ')')+1:])
	return len(f) > 0 && f[0] != "Z"
}

// dbWatchWorld boots with a stand-in database: pg_ctl starts a long-running "postmaster", writes
// its process id to postmaster.pid and notes the start, as pg_ctl -w start does; it exits with the
// code in $FS_PGCTL_EXIT when that file exists. When $FS_DIES exists, it is removed and the
// postmaster has already gone when pg_ctl returns. installedPrograms finds the postmaster while it
// runs. Every postmaster started is ended after the test.
func dbWatchWorld(t *testing.T) (tp *tips, starts, exitFile string) {
	t.Helper()
	dir := t.TempDir()
	starts, exitFile, servers := dir+"/starts", dir+"/exit", dir+"/servers"
	t.Setenv("FS_STARTS", starts)
	t.Setenv("FS_PGCTL_EXIT", exitFile)
	t.Setenv("FS_SERVERS", servers)
	tp = startFailWorld(t, map[string]string{
		"pgsql\\bin\\pg_ctl.exe": `echo start >> "$FS_STARTS"; [ -f "$FS_PGCTL_EXIT" ] && exit $(cat "$FS_PGCTL_EXIT")
if [ -n "$FS_DIES" ] && [ -f "$FS_DIES" ]; then rm -f "$FS_DIES"; true & p=$!; wait $p; echo $p > "$FS_PIDFILE"; exit 0; fi
sleep 60 > /dev/null 2>&1 &
echo $! >> "$FS_SERVERS"; echo $! > "$FS_PIDFILE"; exit 0`,
		"bitcoincashIId.exe": sleeper, "elevenseventyfived.exe": sleeper, "api.exe": sleeper, "stratum.exe": sleeper,
	})
	t.Setenv("FS_PIDFILE", dpath("pgdata", "postmaster.pid"))
	savedPoll, savedExit := dbWatchPoll, processExit
	dbWatchPoll = 50 * time.Millisecond
	processExit = func(pid int) <-chan struct{} {
		done := make(chan struct{})
		go func() {
			for alive(pid) {
				time.Sleep(20 * time.Millisecond)
			}
			close(done)
		}()
		return done
	}
	installedPrograms = func() []runningProgram {
		if pid := postmasterPID(); pid != 0 && alive(pid) {
			return []runningProgram{{pid, "postgres.exe"}}
		}
		return nil
	}
	t.Cleanup(func() {
		mu.Lock()
		stopping = true
		mu.Unlock()
		dbWatching.Wait() // it starts nothing more
		b, _ := os.ReadFile(servers)
		for _, s := range strings.Fields(string(b)) {
			if pid, _ := strconv.Atoi(s); pid > 0 && alive(pid) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		dbWatchPoll, processExit = savedPoll, savedExit
	})
	return tp, starts, exitFile
}

func startsIn(path string) int { b, _ := os.ReadFile(path); return strings.Count(string(b), "start\n") }

// The database server stops on its own (ended from Task Manager or by a cleanup tool, or after a
// crash its recovery failed): it is started again, with the same back-off as the other programs,
// and the tray says so, then "running" once it is back. It stayed down while the tray said
// "running", and Settings and every block record failed until someone restarted Forge Solo.
func TestTheDatabaseIsStartedAgain(t *testing.T) {
	tp, starts, exitFile := dbWatchWorld(t)
	boot()
	if !waitFor(5*time.Second, func() bool { return tp.last() == "Forge Solo: running" }) {
		t.Fatalf("setup: Forge Solo did not come to running: %q\n%s", tp.all(), launcherLog())
	}
	// It cannot start at first (pglog.txt would say why), and then can.
	if err := os.WriteFile(exitFile, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = syscall.Kill(postmasterPID(), syscall.SIGKILL)
	if !waitFor(5*time.Second, func() bool { return strings.Count(launcherLog(), "the database did not start again") >= 2 }) || startsIn(starts) < 3 {
		t.Fatalf("DB-WATCH-RESTART: the database was not started again after it stopped (%d starts):\n%s", startsIn(starts), launcherLog())
	}
	_ = os.Remove(exitFile)
	if !waitFor(5*time.Second, func() bool { return tp.last() == "Forge Solo: running" }) {
		t.Errorf("DB-WATCH-RUNNING-AFTER: once the database was back the tray says %q", tp.last())
	}
	log := launcherLog()
	if !strings.Contains(log, "the database stopped on its own: starting it again in 50ms") || !strings.Contains(log, "the database started again") {
		t.Errorf("DB-WATCH-LOGGED: launcher.log does not say the database stopped and was started again:\n%s", log)
	}
	if !strings.Contains(log, "trying again in 100ms") || !strings.Contains(log, "trying again in 200ms") {
		t.Errorf("DB-WATCH-BACKOFF: the waits between tries did not grow:\n%s", log)
	}
	if !tp.has("Forge Solo: the database stopped on its own and is started again") || !tp.has("Forge Solo: the database could not start again") {
		t.Errorf("DB-WATCH-TRAY: the tray did not say the database stopped, then could not start: %q", tp.all())
	}

	// Once the stop has begun (Quit, or Windows ending the session), a database that goes is the
	// stop's doing: it is not started again.
	n := startsIn(starts)
	mu.Lock()
	stopping = true
	mu.Unlock()
	_ = syscall.Kill(postmasterPID(), syscall.SIGKILL)
	time.Sleep(time.Second)
	if startsIn(starts) != n || strings.Count(launcherLog(), "the database stopped on its own") != 1 {
		t.Errorf("DB-WATCH-NOT-DURING-STOP: the database was taken for stopped on its own, or started again, during the stop:\n%s", launcherLog())
	}
}

// The server has already gone when the watch first looks for it (it crashed just after its start,
// leaving postmaster.pid): it stopped on its own too, and is started again. The watch took it for
// another program's and ended, leaving the database down for the rest of the run.
func TestADatabaseGoneBeforeItIsWatchedIsStartedAgain(t *testing.T) {
	dies := t.TempDir() + "/dies"
	t.Setenv("FS_DIES", dies)
	if err := os.WriteFile(dies, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tp, starts, _ := dbWatchWorld(t)
	boot()
	if !waitFor(5*time.Second, func() bool { return strings.Contains(launcherLog(), "the database started again") }) || startsIn(starts) != 2 {
		t.Fatalf("DB-WATCH-GONE-FIRST: a server gone before the watch held it was not started again (%d starts):\n%s", startsIn(starts), launcherLog())
	}
	if !waitFor(5*time.Second, func() bool { return tp.last() == "Forge Solo: running" }) {
		t.Errorf("DB-WATCH-GONE-FIRST-RUNNING: once the database was back the tray says %q", tp.last())
	}
	// The server started again is watched.
	_ = syscall.Kill(postmasterPID(), syscall.SIGKILL)
	if !waitFor(5*time.Second, func() bool { return startsIn(starts) == 3 }) {
		t.Errorf("DB-WATCH-GONE-FIRST-HELD: the server started again was not watched (%d starts):\n%s", startsIn(starts), launcherLog())
	}
}

// No postmaster.pid after a start that worked (it could not be read a moment): the server is not
// taken for one that stopped. Started again beside the one running, it would fail at every try.
func TestTheDatabaseWatchWithNoPostmasterPID(t *testing.T) {
	_, starts, _ := dbWatchWorld(t)
	t.Setenv("FS_PIDFILE", t.TempDir()+"/pid")
	boot()
	const notWatched = "server of this install's (0), so it is not watched"
	if !waitFor(5*time.Second, func() bool { return strings.Contains(launcherLog(), notWatched) }) {
		t.Fatalf("DB-WATCH-NO-PIDFILE: with no postmaster.pid the watch did not leave the database alone:\n%s", launcherLog())
	}
	time.Sleep(500 * time.Millisecond)
	if startsIn(starts) != 1 {
		t.Errorf("DB-WATCH-NO-PIDFILE: with no postmaster.pid the database was started again (%d starts)", startsIn(starts))
	}
}

// postmaster.pid naming a process that is not this install's database (its number gone to another
// program): that process is not watched, and its end starts nothing.
func TestTheDatabaseWatchWatchesOnlyItsServer(t *testing.T) {
	_, starts, _ := dbWatchWorld(t)
	installedPrograms = func() []runningProgram { return nil }
	boot()
	if !waitFor(5*time.Second, func() bool { return strings.Contains(launcherLog(), "so it is not watched") }) {
		t.Fatalf("DB-WATCH-OURS-ONLY: a process that is not this install's database was watched:\n%s", launcherLog())
	}
	_ = syscall.Kill(postmasterPID(), syscall.SIGKILL)
	time.Sleep(500 * time.Millisecond)
	if startsIn(starts) != 1 {
		t.Errorf("DB-WATCH-OURS-ONLY: the end of another program started the database again (%d starts)", startsIn(starts))
	}
}
