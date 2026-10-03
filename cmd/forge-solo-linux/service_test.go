package main

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestUnitFile(t *testing.T) {
	u := unitFile("0.0.0.0:3080")
	for _, want := range []string{
		"User=forge-solo\n",
		"ExecStart=/opt/forge-solo/forge-solo run --data-dir /var/lib/forge-solo --web 0.0.0.0:3080\n",
		"KillMode=mixed\n", "TimeoutStopSec=180\n", "Restart=on-failure\n", "WantedBy=multi-user.target\n",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
	// Type=exec: a launcher systemd cannot execute fails systemctl restart, not just the service.
	if !strings.Contains(u, "\nType=exec\n") {
		t.Errorf("UNIT-EXEC: the unit is not Type=exec:\n%s", u)
	}
	if strings.Contains(u, "Group=") {
		t.Error("Group= set: the user's own group must apply")
	}
}

// The stop timeout must cover the launcher's own stop allowances, or systemd kills the node
// before it has flushed its chain state.
func TestUnitStopTimeoutCoversGraces(t *testing.T) {
	if total := stratumGrace + apiGrace + nodeGrace; total.Seconds() >= 180 {
		t.Fatalf("graces add up to %s, TimeoutStopSec is 180s", total)
	}
}

func TestReplaceDirInstallsAndUpgrades(t *testing.T) {
	base := t.TempDir()
	src, dst := filepath.Join(base, "rel"), filepath.Join(base, "opt", "forge-solo")
	write := func(p, s string, mode os.FileMode) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(src, "forge-solo"), "v1", 0o700)
	write(filepath.Join(src, "bin", "api"), "api1", 0o755)
	write(filepath.Join(src, "web", "solo.html"), "page1", 0o600)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceDir(src, dst); err != nil {
		t.Fatal(err)
	}
	for p, mode := range map[string]os.FileMode{"forge-solo": 0o755, "bin/api": 0o755, "web/solo.html": 0o644} {
		st, err := os.Stat(filepath.Join(dst, p))
		if err != nil || st.Mode().Perm() != mode {
			t.Fatalf("%s: %v %v, want mode %v", p, st, err, mode)
		}
	}

	// An upgrade replaces the files and drops what the new release no longer has.
	write(filepath.Join(dst, "bin", "stale"), "x", 0o755)
	write(filepath.Join(src, "forge-solo"), "v2", 0o700)
	if err := replaceDir(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "forge-solo")); string(b) != "v2" {
		t.Fatalf("not upgraded: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dst, "bin", "stale")); err == nil {
		t.Fatal("a file the release no longer has survived the upgrade")
	}
	for _, p := range []string{dst + ".new", dst + ".old"} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s left behind", p)
		}
	}
}

// Under sudo with a umask of 077 (or 027) every directory of the install came out 0700, so the
// service user could not reach the program and systemd failed it with 203/EXEC. The directories
// are 0755 whatever the umask, a missing parent (/opt) is made 0755 too, and an existing parent
// keeps the mode its administrator gave it.
func TestReplaceDirModesUnderUmask077(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	base := t.TempDir()
	src := filepath.Join(base, "rel")
	for p, mode := range map[string]os.FileMode{"forge-solo": 0o700, "bin/api": 0o700, "web/solo.html": 0o600, "web/js/app.js": 0o600} {
		if err := os.MkdirAll(filepath.Join(src, filepath.Dir(p)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, p), []byte(p), mode); err != nil {
			t.Fatal(err)
		}
	}
	dst := filepath.Join(base, "opt", "forge-solo") // base/opt does not exist, as /opt on some systems
	if err := replaceDir(src, dst); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{filepath.Join(base, "opt"), dst, filepath.Join(dst, "bin"), filepath.Join(dst, "web"), filepath.Join(dst, "web", "js")} {
		if st, err := os.Stat(d); err != nil || st.Mode().Perm() != 0o755 {
			t.Errorf("UMASK-DIR: %s is %v (%v), want 0755", d, st.Mode().Perm(), err)
		}
	}
	for p, want := range map[string]os.FileMode{"forge-solo": 0o755, "bin/api": 0o755, "web/solo.html": 0o644, "web/js/app.js": 0o644} {
		if st, err := os.Stat(filepath.Join(dst, p)); err != nil || st.Mode().Perm() != want {
			t.Errorf("UMASK-FILE: %s is %v (%v), want %v", p, st.Mode().Perm(), err, want)
		}
	}

	kept := filepath.Join(base, "kept")
	if err := os.Mkdir(kept, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(kept, 0o750); err != nil { // past the umask
		t.Fatal(err)
	}
	if err := replaceDir(src, filepath.Join(kept, "forge-solo")); err != nil {
		t.Fatalf("UMASK-PARENT-KEPT: install under an existing parent: %v", err)
	}
	if st, err := os.Stat(kept); err != nil || st.Mode().Perm() != 0o750 {
		t.Errorf("UMASK-PARENT-KEPT: an existing parent became %v (%v), want it left 0750", st.Mode().Perm(), err)
	}
}

// sleeper is another process: one that holds no socket.
func sleeper(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}

func TestPidHoldsListener(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:0", ":0"} {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		if !pidHoldsListener(os.Getpid(), port) {
			t.Errorf("LISTENER-OWN: this process listens on %s but was not found holding it", l.Addr())
		}
		if pidHoldsListener(sleeper(t), port) {
			t.Errorf("LISTENER-OTHER: another process was found holding %s", l.Addr())
		}
		l.Close()
		if pidHoldsListener(os.Getpid(), port) {
			t.Errorf("LISTENER-CLOSED: a closed listener on %d was still found", port)
		}
	}
}

// install-service succeeds only once the service's own launcher serves the dashboard. A
// dashboard answering at the address is not enough: a Forge Solo started by hand may be
// answering there while the service fails.
func TestWaitServiceServing(t *testing.T) {
	dash := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/solo", http.StatusFound)
	}))
	defer dash.Close()
	web := strings.TrimPrefix(dash.URL, "http://")
	is := func(st serviceStatus) func() (serviceStatus, error) {
		return func() (serviceStatus, error) { return st, nil }
	}

	// This process plays the service's launcher: it holds the listener and answers.
	if err := waitServiceServing(web, is(serviceStatus{pid: os.Getpid()}), 5*time.Second); err != nil {
		t.Errorf("SERVE-OK: the service serving its dashboard was not accepted: %v", err)
	}

	// The service's launcher is another process; this one, answering, plays a Forge Solo started
	// by hand.
	if err := waitServiceServing(web, is(serviceStatus{pid: sleeper(t)}), 1500*time.Millisecond); !errors.Is(err, errServiceNotServing) {
		t.Errorf("SERVE-FOREGROUND: a dashboard answered by another process was taken for the service: %v", err)
	}

	// The launcher holds a listener that does not answer (it has not started serving).
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := waitServiceServing(l.Addr().String(), is(serviceStatus{pid: os.Getpid()}), time.Second); !errors.Is(err, errServiceNotServing) {
		t.Errorf("SERVE-SILENT: a listener that does not answer was taken for a serving dashboard: %v", err)
	}

	// A service that stopped (failed, or waiting to be started again) is reported at once.
	t0 := time.Now()
	err = waitServiceServing(web, is(serviceStatus{stopped: true}), 20*time.Second)
	if !errors.Is(err, errServiceNotServing) {
		t.Errorf("SERVE-STOPPED: a stopped service: %v", err)
	}
	if d := time.Since(t0); d > 3*time.Second {
		t.Errorf("SERVE-FAST: a stopped service was waited on for %s", d)
	}
}

func TestProbeHost(t *testing.T) {
	for web, want := range map[string]string{"127.0.0.1:3080": "127.0.0.1", "0.0.0.0:3080": "127.0.0.1", ":3080": "127.0.0.1",
		"[::]:3080": "127.0.0.1", "localhost:3080": "127.0.0.1", "192.168.1.5:3080": "192.168.1.5", "[::1]:3080": "::1"} {
		if got := probeHost(web); got != want {
			t.Errorf("%s: %q, want %q", web, got, want)
		}
	}
}

// Before installing, a port held by anything but the (stopped) service is named, with what to
// do when it is a Forge Solo started by hand.
func TestCheckInstallPortsNamesAForegroundCopy(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	err = checkInstallPorts(l.Addr().String())
	if err == nil {
		t.Fatal("INSTALL-PORTS: no error with the dashboard address taken")
	}
	if !strings.Contains(err.Error(), "started yourself") || !strings.Contains(err.Error(), "not carried over") {
		t.Errorf("INSTALL-FOREGROUND: the error does not say to stop a Forge Solo started by hand: %v", err)
	}
	saved := publicPorts
	t.Cleanup(func() { publicPorts = saved })
	publicPorts = publicPorts[:0:0] // the public ports free: only the dashboard address is taken
	if err := checkInstallPorts(l.Addr().String()); err == nil || !strings.Contains(err.Error(), l.Addr().String()) {
		t.Errorf("INSTALL-PORTS: the error does not name the dashboard address: %v", err)
	}
	if err := checkInstallPorts("127.0.0.1:0"); err != nil {
		t.Errorf("INSTALL-PORTS: free ports refused: %v", err)
	}

	// A public port taken, the dashboard address free.
	taken := usePublicPorts(t)
	err = checkInstallPorts("127.0.0.1:0")
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(taken)) || !strings.Contains(err.Error(), "started yourself") {
		t.Errorf("INSTALL-PUBLIC-PORT: with port %d taken, install-service did not name it and say to stop a copy started by hand: %v", taken, err)
	}
}

// The firewall hint names the dashboard's port when --web listens beyond this machine.
func TestFirewallPorts(t *testing.T) {
	base := []int{stratumPort, rentalPort, p2pPort}
	for web, want := range map[string][]int{
		"127.0.0.1:3080": base, "localhost:3080": base, "[::1]:3080": base,
		"0.0.0.0:3080": append(append([]int{}, base...), 3080), "192.168.1.5:4000": append(append([]int{}, base...), 4000),
	} {
		if got := firewallPorts(web); !reflect.DeepEqual(got, want) {
			t.Errorf("FW-WEB: --web %s: ports %v, want %v", web, got, want)
		}
	}
}
