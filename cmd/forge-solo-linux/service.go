package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	serviceName = "forge-solo"
	serviceUser = "forge-solo"
	serviceDir  = "/opt/forge-solo"
	serviceData = "/var/lib/forge-solo"
	unitPath    = "/etc/systemd/system/forge-solo.service"
)

// serviceStartWait is how long install-service gives the restarted service to serve its
// dashboard: the launcher only starts the node and the API before it does, so this is plenty
// on the smallest board.
const serviceStartWait = 30 * time.Second

// unitFile is the systemd unit install-service writes.
//
// Type=exec: systemctl start and restart return once the launcher has really been executed, so a
// launcher systemd cannot execute (in a directory the service user cannot enter, say) fails the
// install instead of looking started.
//
// KillMode=mixed: a stop sends SIGTERM to the launcher alone, which stops the stratum, the API
// and the node in that order (the node flushes its chain state). systemd sends SIGKILL to
// whatever is still running when TimeoutStopSec runs out, and also as soon as the launcher has
// exited: so if the launcher itself dies (killed outright, or a crash), the node is killed at
// once, in the middle of its own shutdown, and replays its last blocks at its next start. With
// the default, systemd would signal them all at once and the order would be lost.
func unitFile(web string) string {
	return fmt.Sprintf(`[Unit]
Description=Forge Solo: BCH2 solo mining (node, stratum and dashboard)
Documentation=https://github.com/BitcoincashII/forge-solo
Wants=network-online.target
After=network-online.target

[Service]
Type=exec
User=%[1]s
ExecStart=%[2]s/forge-solo run --data-dir %[3]s --web %[4]s
KillMode=mixed
TimeoutStopSec=180
# Mining software on a machine that is often doing other things: below them for CPU and disk.
Nice=10
CPUWeight=50
IOWeight=50
Restart=on-failure
RestartSec=10
LimitNOFILE=8192
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true

[Install]
WantedBy=multi-user.target
`, serviceUser, serviceDir, serviceData, web)
}

func haveSystemd() bool {
	st, err := os.Stat("/run/systemd/system")
	return err == nil && st.IsDir()
}

func runLoud(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// installService copies this release to /opt/forge-solo, creates the forge-solo system user and
// /var/lib/forge-solo, and installs, enables and (re)starts the systemd service. Run again from
// a newer release, it upgrades in place and keeps the data and the dashboard address. It succeeds
// only once the service is serving its dashboard.
func installService(args []string) error {
	web, given, err := parseInstallFlags(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := checkKernel(getrandom(), kernelRelease()); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("install-service needs root: run it with sudo")
	}
	if !haveSystemd() {
		return errors.New("this system does not run systemd. Start Forge Solo from your init system instead: " +
			"as an unprivileged user that owns its data directory, run `forge-solo run --data-dir DIR`, and stop it " +
			"with SIGTERM, allowing up to 3 minutes. See README.md")
	}
	src, err := installDir()
	if err != nil {
		return err
	}
	if err := checkRelease(src); err != nil {
		return err
	}
	return install(systemHost{src: src}, os.Stdout, web, given)
}

// parseInstallFlags reads install-service's flags: the dashboard address, and whether --web gave it.
func parseInstallFlags(args []string) (web string, given bool, err error) {
	fs := flag.NewFlagSet("install-service", flag.ContinueOnError)
	fs.StringVar(&web, "web", defaultWeb, "dashboard address, host:port; anything but 127.0.0.1 asks for a password")
	if err := fs.Parse(args); err != nil {
		return "", false, err
	}
	fs.Visit(func(f *flag.Flag) { given = given || f.Name == "web" })
	return web, given, checkWebAddr(web)
}

// host is the machine install-service works on (the tests stand in another).
type host interface {
	unit() (text string, installed bool) // the service's unit file, if it is installed
	active() bool                        // the service is running
	systemctl(args ...string) error
	checkPorts(web string) (left []int, err error) // checkInstallPorts
	checkWeb(web string) error                     // checkDashboardAddr
	copyRelease() error                            // the release to /opt/forge-solo
	ensureUser() error
	ensureData() error
	writeUnit(text string) error
	waitServing(web string) error // waitServiceServing
	journalTail()
	firewallHint(web string)
}

// install installs, or upgrades, the service on h, and reports to out. web is the dashboard
// address; when --web did not give it, the installed service keeps its own.
//
// What can fail is checked while a running service still runs. If something fails after install
// has stopped it, it is started again: install used to leave it stopped, and mining with it,
// without a word.
func install(h host, out io.Writer, web string, given bool) (err error) {
	oldUnit, installed := h.unit()
	old := ""
	if installed {
		old = unitWeb(oldUnit)
	}
	web, note := chooseWeb(web, given, old)
	running := installed && h.active()
	servedAt := old // where the installed unit serves its dashboard
	if checkWebAddr(servedAt) != nil {
		servedAt = defaultWeb
	}

	// A running service holds Forge Solo's ports and its dashboard's: only a new dashboard port
	// can be checked before it stops. One that is not running holds none.
	switch {
	case !running:
		if _, err := h.checkPorts(web); err != nil {
			return err
		}
	case portOf(web) != portOf(servedAt):
		if err := h.checkWeb(web); err != nil {
			return err
		}
	}
	if err := h.ensureUser(); err != nil {
		return err
	}
	if note != "" {
		fmt.Fprintln(out, note)
	}

	// Stop the service first: its files are about to be replaced, and it holds the ports. Also
	// when it is not running: one waiting to be started again would start in the middle of this.
	restarting := false // from then on a failure is the new service's own
	if installed {
		if running {
			fmt.Fprintln(out, "Stopping the running Forge Solo service…")
		}
		if err := h.systemctl("stop", serviceName); err != nil {
			return err
		}
		if running {
			defer func() {
				if err != nil && !restarting {
					err = startAgain(h, servedAt, err)
				}
			}()
		}
	}
	// Whatever holds Forge Solo's ports now is not the service, and the service would fail to
	// start beside it. Most often it is a Forge Solo started by hand.
	left, err := h.checkPorts(web)
	if err != nil {
		return err
	}
	for _, port := range left {
		fmt.Fprintf(out, "Note: %s.\n", leftOutNote(port))
	}
	if err := h.copyRelease(); err != nil {
		return err
	}
	if err := h.ensureData(); err != nil {
		return err
	}
	if err := h.writeUnit(unitFile(web)); err != nil {
		return err
	}
	servedAt = web
	for _, a := range [][]string{{"daemon-reload"}, {"enable", serviceName}} {
		if err := h.systemctl(a...); err != nil {
			return err
		}
	}
	restarting = true
	if err := h.systemctl("restart", serviceName); err != nil {
		h.journalTail()
		return fmt.Errorf("the %s service could not be started (%v). Its last log lines are above: fix what they say, then run install-service again", serviceName, err)
	}
	if err := h.waitServing(web); err != nil {
		h.journalTail()
		return fmt.Errorf("%v. Its last log lines are above: fix what they say, then run install-service again", err)
	}
	rent := fmt.Sprintf("(rentals: %d)", rentalPort)
	if len(left) > 0 {
		rent = noRentals(rentalPort)
	}
	fmt.Fprintf(out, `
Forge Solo is installed and running as the %[1]s service.

  Dashboard:  %[2]s
  Miners:     stratum+tcp://<this machine's address>:%[3]d  %[4]s
  Data:       %[5]s
  Logs:       journalctl -u %[1]s -f   and %[5]s/logs/
  Stop/start: sudo systemctl stop %[1]s  /  sudo systemctl start %[1]s
`, serviceName, dashboardURL(web), stratumPort, rent, serviceData)
	fmt.Fprintf(out, "  Settings:   saving a change asks for DASHBOARD_PASSWORD in %s/secrets.env\n", serviceData)
	if webNeedsPassword(web) {
		fmt.Fprintf(out, "  Password:   user forge, DASHBOARD_PASSWORD in %s/secrets.env\n", serviceData)
	} else {
		fmt.Fprintf(out, "  From another computer: ssh -L 3080:%s user@this-machine, then open http://127.0.0.1:3080\n", web)
	}
	h.firewallHint(web)
	return nil
}

// startAgain starts the service install stopped before it failed with cause, and adds to cause
// whether it runs again: web is where the installed unit serves its dashboard.
func startAgain(h host, web string, cause error) error {
	if h.systemctl("start", serviceName) == nil && h.waitServing(web) == nil {
		return fmt.Errorf("%w\nThe %s service has been started again", cause, serviceName)
	}
	return fmt.Errorf("%w\nThe %s service is stopped, so Forge Solo is not mining. Start it again with: sudo systemctl start %s",
		cause, serviceName, serviceName)
}

// portOf is the port of a host:port address.
func portOf(addr string) string {
	_, port, _ := net.SplitHostPort(addr)
	return port
}

// unitWeb is the --web address in a unit file's ExecStart line: "" if it has none.
func unitWeb(unit string) string {
	for _, line := range strings.Split(unit, "\n") {
		cmd, ok := strings.CutPrefix(strings.TrimSpace(line), "ExecStart=")
		if !ok {
			continue
		}
		f := strings.Fields(cmd)
		for i, a := range f {
			if a == "--web" && i+1 < len(f) {
				return f[i+1]
			}
			if w, ok := strings.CutPrefix(a, "--web="); ok {
				return w
			}
		}
	}
	return ""
}

// chooseWeb is the dashboard address the service gets, and what to say about it: the one --web
// gave, else the installed service's own (old), else the default. An upgrade used to move a
// dashboard served to the network back to 127.0.0.1, and the network lost it.
func chooseWeb(web string, given bool, old string) (string, string) {
	switch {
	case old == "":
		return web, ""
	case checkWebAddr(old) != nil:
		if given {
			return web, ""
		}
		return web, fmt.Sprintf("Dashboard address: %s (the installed service's %q is not a host:port).", web, old)
	case !given:
		return old, fmt.Sprintf("Dashboard address: %s, kept from the installed service (give --web to change it).", old)
	case web != old:
		return web, fmt.Sprintf("Dashboard address: %s, changed from %s.", web, old)
	}
	return web, ""
}

// systemHost is this machine; src is the release being installed.
type systemHost struct{ src string }

func (systemHost) unit() (string, bool) {
	b, err := os.ReadFile(unitPath)
	return string(b), err == nil
}

func (systemHost) active() bool {
	return exec.Command("systemctl", "is-active", "--quiet", serviceName).Run() == nil
}

func (systemHost) systemctl(args ...string) error       { return runLoud("systemctl", args...) }
func (systemHost) checkPorts(web string) ([]int, error) { return checkInstallPorts(web) }
func (systemHost) checkWeb(web string) error            { return checkDashboardAddr(web) }
func (systemHost) ensureUser() error                    { return ensureServiceUser() }
func (systemHost) ensureData() error                    { return ensureDataDir(serviceData, serviceUser) }
func (systemHost) writeUnit(text string) error          { return writeFileAtomic(unitPath, []byte(text), 0o644) }
func (systemHost) journalTail()                         { journalTail() }
func (systemHost) firewallHint(web string)              { firewallHint(web) }

func (systemHost) waitServing(web string) error {
	return waitServiceServing(web, serviceState, serviceStartWait)
}

func (h systemHost) copyRelease() error {
	if filepath.Clean(h.src) == serviceDir {
		return nil
	}
	fmt.Printf("Installing %s to %s…\n", h.src, serviceDir)
	return replaceDir(h.src, serviceDir)
}

// foregroundHint follows a port that is taken when the service is about to be installed, when it
// is one a Forge Solo started by hand listens on.
const foregroundHint = "If that is a Forge Solo you started yourself (./forge-solo in a terminal), stop it first (Ctrl-C), " +
	"then run install-service again. The service keeps its own data in " + serviceData + ": the payout address and " +
	"settings you saved in that copy are not carried over, so save them again on the service's dashboard."

// checkInstallPorts fails, naming the port, when another program holds one of the ports the
// service cannot run without: it would start and fail at once. It returns the optional ports
// another program holds, which the service runs without.
func checkInstallPorts(web string) (left []int, err error) {
	left, err = checkPublicPorts()
	if err != nil {
		return nil, fmt.Errorf("%w\n%s", err, foregroundHint)
	}
	return left, checkDashboardAddr(web)
}

// checkDashboardAddr fails when another program listens on the dashboard's address. Only on the
// dashboard's own port can that be a Forge Solo started by hand.
func checkDashboardAddr(web string) error {
	l, err := net.Listen("tcp", web)
	if err != nil {
		if portOf(web) == portOf(defaultWeb) {
			return fmt.Errorf("the dashboard address %s is already in use by another program (%v).\n%s", web, err, foregroundHint)
		}
		return fmt.Errorf("the dashboard address %s is already in use by another program (%v): stop that program, "+
			"or give the dashboard another address with --web", web, err)
	}
	_ = l.Close()
	return nil
}

// errServiceNotServing: after a (re)start, the service itself is not serving its dashboard.
var errServiceNotServing = errors.New("the forge-solo service is not serving its dashboard")

// serviceStatus is the service as systemd sees it: its main process, the launcher (0: none),
// and whether it has stopped (failed, or waiting to be started again after failing).
type serviceStatus struct {
	pid     int
	stopped bool
}

// waitServiceServing waits until the service's own launcher holds the dashboard's listening
// socket and the dashboard answers there. A dashboard answering at the address proves nothing by
// itself: a Forge Solo started by hand may be the one answering while the service fails. The
// launcher serves only once it has started the node and the API.
func waitServiceServing(web string, status func() (serviceStatus, error), timeout time.Duration) error {
	_, portStr, err := net.SplitHostPort(web)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return err
	}
	url := "http://" + net.JoinHostPort(probeHost(web), portStr) + "/"
	client := &http.Client{Timeout: 2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	deadline := time.Now().Add(timeout)
	for {
		var why string
		st, err := status()
		switch {
		case err != nil:
			why = err.Error()
		case st.stopped:
			return fmt.Errorf("%w at %s: it stopped", errServiceNotServing, web)
		case st.pid <= 0:
			why = "it is not running"
		case !pidHoldsListener(st.pid, port):
			why = fmt.Sprintf("its launcher (process %d) is not listening on port %d", st.pid, port)
		default:
			err := probeDashboard(client, url)
			if err == nil {
				return nil
			}
			why = "the dashboard does not answer: " + err.Error()
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%w at %s after %s: %s", errServiceNotServing, web, timeout, why)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// probeDashboard asks the dashboard for its first page. Any answer will do (a redirect, or a
// request for the password): it shows the launcher is serving.
func probeDashboard(client *http.Client, url string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// probeHost is where this machine reaches a dashboard listening on web.
func probeHost(web string) string {
	host, _, _ := net.SplitHostPort(web)
	if host == "" || strings.EqualFold(host, "localhost") {
		return "127.0.0.1"
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return "127.0.0.1" // Go listens dual-stack on [::] too
	}
	return host
}

// pidHoldsListener reports whether process pid has open a TCP socket listening on port.
func pidHoldsListener(pid, port int) bool {
	inodes := listenerInodes(port)
	if len(inodes) == 0 {
		return false
	}
	dir := "/proc/" + strconv.Itoa(pid) + "/fd"
	fds, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, fd := range fds {
		l, err := os.Readlink(dir + "/" + fd.Name())
		if err == nil && strings.HasPrefix(l, "socket:[") && inodes[strings.TrimSuffix(strings.TrimPrefix(l, "socket:["), "]")] {
			return true
		}
	}
	return false
}

// listenerInodes are the inodes of the TCP sockets listening on port, IPv4 and IPv6.
func listenerInodes(port int) map[string]bool {
	inodes := map[string]bool{}
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			// sl local_address rem_address st tx_queue:rx_queue tr:tm->when retrnsmt uid timeout inode
			fields := strings.Fields(line)
			if len(fields) < 10 || fields[3] != "0A" { // 0A: listening
				continue
			}
			i := strings.LastIndexByte(fields[1], ':')
			if i < 0 {
				continue
			}
			if p, err := strconv.ParseUint(fields[1][i+1:], 16, 16); err == nil && int(p) == port {
				inodes[fields[9]] = true
			}
		}
	}
	return inodes
}

// serviceState asks systemd how the service is.
func serviceState() (serviceStatus, error) {
	out, err := exec.Command("systemctl", "show", "-p", "MainPID", "-p", "ActiveState", "-p", "SubState", serviceName).Output()
	if err != nil {
		return serviceStatus{}, fmt.Errorf("systemctl show %s: %v", serviceName, err)
	}
	var st serviceStatus
	for _, line := range strings.Split(string(out), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch {
		case k == "MainPID":
			st.pid, _ = strconv.Atoi(v)
		case k == "ActiveState" && (v == "failed" || v == "inactive"), k == "SubState" && v == "auto-restart":
			st.stopped = true
		}
	}
	return st, nil
}

// journalTail prints the service's last log lines, which say why it is not running.
func journalTail() {
	fmt.Fprintf(os.Stderr, "\nThe last log lines of the %s service (journalctl -u %s):\n", serviceName, serviceName)
	cmd := exec.Command("journalctl", "-u", serviceName, "-n", "30", "--no-pager")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	_ = cmd.Run()
	fmt.Fprintln(os.Stderr)
}

// uninstallService stops and removes the service. The program and the data stay; it says where.
func uninstallService(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("uninstall-service takes no arguments")
	}
	if os.Geteuid() != 0 {
		return errors.New("uninstall-service needs root: run it with sudo")
	}
	if !haveSystemd() {
		return errors.New("this system does not run systemd, so there is no service to remove")
	}
	if _, err := os.Stat(unitPath); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s is not installed", unitPath)
	}
	if err := runLoud("systemctl", "disable", "--now", serviceName); err != nil {
		return err
	}
	if err := os.Remove(unitPath); err != nil {
		return err
	}
	if err := runLoud("systemctl", "daemon-reload"); err != nil {
		return err
	}
	fmt.Printf(`The Forge Solo service is stopped and removed. Still on disk:
  %[1]s  (the program)
  %[2]s  (the chain, the database with your settings and blocks, and secrets.env)
Remove them with: sudo rm -r %[1]s %[2]s   and the user with: sudo userdel %[3]s
`, serviceDir, serviceData, serviceUser)
	return nil
}

// replaceDir copies the release in src to dst through dst.new, so a failed copy leaves the old
// install whole.
func replaceDir(src, dst string) error {
	if err := mkdirParents(filepath.Dir(dst)); err != nil {
		return err
	}
	tmp, old := dst+".new", dst+".old"
	_ = os.RemoveAll(tmp)
	if err := copyRelease(src, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	_ = os.RemoveAll(old)
	if _, err := os.Stat(dst); err == nil {
		if err := os.Rename(dst, old); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Rename(old, dst)
		return err
	}
	return os.RemoveAll(old)
}

// mkdirParents creates dir and any missing parent 0755 (a missing /opt, say). A directory that
// is already there keeps its mode: it is the administrator's.
func mkdirParents(dir string) error {
	var missing []string
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			break
		}
		missing = append(missing, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := mkdir0755(missing[i]); err != nil {
			return err
		}
	}
	return nil
}

// mkdir0755 creates dir 0755 whatever the umask. The service user must be able to enter every
// directory of the install: under sudo with a umask of 077 or 027 they came out 0700 or 0750,
// and systemd could not start the service (203/EXEC) while the install said it was running.
func mkdir0755(dir string) error {
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	return os.Chmod(dir, 0o755)
}

// releaseFiles are the release's own files, as scripts/linux/build-release.sh stages them, besides
// bin/COPYING-* and web/. A release directory can hold more: the data directory of a Forge Solo run
// from it, with secrets.env and the database, which a copy in /opt would make readable by every
// account on the machine.
var releaseFiles = []string{"forge-solo", "LICENSE", "README.md",
	"bin/bitcoincashIId", "bin/bitcoincashII-cli", "bin/stratum", "bin/api"}

// copyRelease copies the release's own files from src to dst, and nothing else there.
func copyRelease(src, dst string) error {
	files := append([]string(nil), releaseFiles...)
	licenses, err := filepath.Glob(filepath.Join(src, "bin", "COPYING-*"))
	if err != nil {
		return err
	}
	for _, l := range licenses {
		files = append(files, filepath.Join("bin", filepath.Base(l)))
	}
	for _, d := range []string{dst, filepath.Join(dst, "bin")} {
		if err := mkdir0755(d); err != nil {
			return err
		}
	}
	for _, f := range files {
		info, err := os.Lstat(filepath.Join(src, f))
		if errors.Is(err, os.ErrNotExist) {
			continue // checkRelease has made sure of the ones it cannot run without
		}
		if err != nil {
			return err
		}
		if err := copyEntry(filepath.Join(src, f), filepath.Join(dst, f), info); err != nil {
			return err
		}
	}
	return copyTree(filepath.Join(src, "web"), filepath.Join(dst, "web"))
}

// copyTree copies a directory of the release: regular files and directories, root-owned, not
// writable by others.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		return copyEntry(p, filepath.Join(dst, rel), info)
	})
}

// copyEntry copies a directory (empty) or a regular file of the release: 0755, or 0644 for a file
// that is not executable.
func copyEntry(src, dst string, info os.FileInfo) error {
	switch {
	case info.IsDir():
		return mkdir0755(dst)
	case info.Mode().IsRegular():
		mode := os.FileMode(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		return copyFile(src, dst, mode)
	default:
		return nil // no links or devices in a release
	}
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

// ensureServiceUser creates the unprivileged system user the service runs as.
func ensureServiceUser() error {
	if exec.Command("id", "-u", serviceUser).Run() == nil {
		return nil
	}
	shell := "/bin/false"
	for _, s := range []string{"/usr/sbin/nologin", "/sbin/nologin"} {
		if _, err := os.Stat(s); err == nil {
			shell = s
			break
		}
	}
	fmt.Printf("Creating the %s system user…\n", serviceUser)
	if _, err := exec.LookPath("useradd"); err == nil {
		return runLoud("useradd", "--system", "--user-group", "--home-dir", serviceData, "--no-create-home",
			"--shell", shell, serviceUser)
	}
	if _, err := exec.LookPath("adduser"); err == nil { // BusyBox
		return runLoud("adduser", "-S", "-D", "-H", "-h", serviceData, "-s", shell, serviceUser)
	}
	return fmt.Errorf("neither useradd nor adduser is available: create a system user named %s, then run this again", serviceUser)
}

// ensureDataDir creates the data directory for the service user, 0700.
func ensureDataDir(dir, user string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	out, err := exec.Command("id", "-u", user).Output()
	if err != nil {
		return fmt.Errorf("id -u %s: %w", user, err)
	}
	uid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return err
	}
	out, err = exec.Command("id", "-g", user).Output()
	if err != nil {
		return fmt.Errorf("id -g %s: %w", user, err)
	}
	gid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return err
	}
	// Everything under it: a data directory first used by `sudo forge-solo run` is root's, and a
	// run as root may have left root's files in it. This gives them all back to the service.
	if err := filepath.Walk(dir, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, uid, gid)
	}); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// firewallPorts are the TCP ports a host firewall must let in: the miners', the rentals', the
// BCH2 peers', and the dashboard's when --web listens beyond this machine.
func firewallPorts(web string) []int {
	ports := []int{stratumPort, rentalPort, p2pPort}
	if webNeedsPassword(web) {
		if _, p, err := net.SplitHostPort(web); err == nil {
			if n, err := strconv.Atoi(p); err == nil && n > 0 {
				ports = append(ports, n)
			}
		}
	}
	return ports
}

// firewallHint names the ports to open when a host firewall that blocks them by default is on.
func firewallHint(web string) {
	ports := firewallPorts(web)
	names := make([]string, len(ports))
	add, allow := make([]string, len(ports)), make([]string, len(ports))
	for i, p := range ports {
		names[i] = strconv.Itoa(p)
		add[i] = fmt.Sprintf("--add-port=%d/tcp", p)
		allow[i] = fmt.Sprintf("sudo ufw allow %d/tcp", p)
	}
	list := strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	what := "for miners and peers"
	if len(ports) > 3 {
		what = "for miners, peers and the dashboard"
	}
	if exec.Command("firewall-cmd", "--state").Run() == nil {
		fmt.Printf("\nfirewalld is on and blocks incoming connections: open TCP %s %s:\n"+
			"  sudo firewall-cmd --permanent %s && sudo firewall-cmd --reload\n", list, what, strings.Join(add, " "))
		return
	}
	if out, err := exec.Command("ufw", "status").Output(); err == nil && strings.Contains(string(out), "Status: active") {
		fmt.Printf("\nufw is on: open TCP %s %s:\n  %s\n", list, what, strings.Join(allow, " && "))
	}
}
