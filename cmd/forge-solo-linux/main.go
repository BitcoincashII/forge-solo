// Forge Solo for Linux runs the BCH2 node, the Forge Solo stratum and the dashboard API from one
// directory as supervised programs, and serves the dashboard. It is BCH2 only: there is no 1175
// node, so 1175 merge-mining is off and the dashboard leaves it out.
//
// A release directory holds this launcher, bin/ (bitcoincashIId, bitcoincashII-cli, stratum, api)
// and web/. Everything it writes goes to the data directory: the chain, the database (settings,
// blocks), secrets.env and logs/.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// version is set at build time: -ldflags "-X main.version=1.0.12".
var version = "dev"

const usageText = `Forge Solo %s for Linux: BCH2 solo and TIDES mining with your own node.

Usage:
  forge-solo [run] [--data-dir DIR] [--web HOST:PORT] [--reindex]
      Run in the foreground until Ctrl-C or SIGTERM (allow it 3 minutes to stop cleanly), as an
      ordinary user: it needs no root.
  sudo forge-solo install-service [--web HOST:PORT]
      Install this release to /opt/forge-solo as a systemd service, data in /var/lib/forge-solo.
      Run it again from a newer release to upgrade; the data and the dashboard address are kept.
  sudo forge-solo uninstall-service
      Stop and remove the service (the program and the data stay).
  forge-solo cli [--data-dir DIR] COMMAND...
      Run a bitcoincashII-cli command against the running node, e.g. forge-solo cli getblockcount
  forge-solo version

Options:
  --data-dir DIR    where the chain, database, settings and logs are kept
                    (default: ~/.local/share/forge-solo; as root, and for the service,
                    /var/lib/forge-solo)
  --web HOST:PORT   dashboard address (default 127.0.0.1:3080). Anything but 127.0.0.1 asks for a
                    password: user forge, DASHBOARD_PASSWORD from secrets.env in the data directory.
                    Saving a change in Settings asks for that password wherever the dashboard listens.
  --reindex         rebuild the node's chain state from the blocks on disk, at this start only.
                    Forge Solo does this by itself, once in a run, when the node stops with
                    "Error opening block database" or "Corrupted block database detected"

Miners connect to port 3333 (NiceHash and MiningRigRentals: 3335); BCH2 peers to 8339.
`

func usage(w io.Writer) { fmt.Fprintf(w, usageText, version) }

func main() {
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		usage(os.Stdout)
		return
	}
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "run":
		err = runCmd(args)
	case "install-service":
		err = installService(args)
	case "uninstall-service":
		err = uninstallService(args)
	case "cli":
		err = cliCmd(args)
	case "version":
		fmt.Printf("Forge Solo %s for Linux\n", version)
	case "help":
		usage(os.Stdout)
	default:
		usage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "forge-solo: "+err.Error())
		os.Exit(1)
	}
}

// installDir is the release directory: the one this executable is in, links resolved.
func installDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

// checkRelease makes sure the release directory is whole before anything starts.
func checkRelease(dir string) error {
	for _, f := range []string{"bin/bitcoincashIId", "bin/bitcoincashII-cli", "bin/stratum", "bin/api", "web/solo.html"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			return fmt.Errorf("%s is missing from %s: unpack the whole release and run forge-solo from it", f, dir)
		}
	}
	return nil
}

// getrandom asks the kernel for random bytes the way the BCH2 node does at every start (a test
// stands in another kernel).
var getrandom = func() error {
	_, err := unix.Getrandom(make([]byte, 1), unix.GRND_NONBLOCK)
	return err
}

// kernelRelease is what uname -r prints.
func kernelRelease() string {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return "unknown"
	}
	return unix.ByteSliceToString(u.Release[:])
}

// checkKernel refuses a system the BCH2 node cannot start on: given what getrandom returned, and
// the kernel's release. The node takes its randomness from getrandom, which came in Linux 3.17,
// and without it aborts at every start, before it has written a line: Forge Solo started it again
// and again while the dashboard waited for it.
func checkKernel(err error, release string) error {
	switch {
	case err == nil, errors.Is(err, unix.EAGAIN), errors.Is(err, unix.EINTR):
		return nil // EAGAIN: the kernel has it, but it is not ready yet (early at boot); the node waits
	case errors.Is(err, unix.ENOSYS):
		return fmt.Errorf("this kernel is Linux %s, and Forge Solo needs Linux 3.17 or newer: the BCH2 node stops "+
			"at once on an older one (it needs the getrandom system call). Run it on a newer kernel", release)
	default:
		return fmt.Errorf("this system does not let programs use the getrandom system call (%v), and the BCH2 node "+
			"stops at once without it. A container or sandbox that blocks it is the usual cause", err)
	}
}

func checkWebAddr(web string) error {
	if _, port, err := net.SplitHostPort(web); err != nil || port == "" {
		return fmt.Errorf("--web %q: give a host and port, e.g. 127.0.0.1:3080 or 0.0.0.0:3080", web)
	}
	return nil
}

// lockDataDir holds an exclusive lock on the data directory for as long as this process runs, so
// two copies can never share one chain, database and set of ports.
func lockDataDir(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, "forge-solo.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another Forge Solo is already running with the data directory %s", dir)
	}
	return f, nil
}

// publicPorts are the ports Forge Solo listens on for other machines (a variable so that the tests
// can use free ones: a machine running Forge Solo already holds these). Forge Solo cannot mine
// without a required one. Without the rental port it runs without rentals, as on Windows.
var publicPorts = []struct {
	port     int
	what     string
	required bool
}{{stratumPort, "The miner port", true}, {rentalPort, "The rental port", false}, {p2pPort, "The BCH2 peer port", true}}

// checkPublicPorts fails early, and says which, when a required public port is taken: the
// programs would otherwise start and run without it (the node, for one, keeps going without its
// P2P listener). It returns the optional ports another program holds: Forge Solo runs without them.
func checkPublicPorts() (left []int, err error) {
	for _, p := range publicPorts {
		l, err := net.Listen("tcp", ":"+strconv.Itoa(p.port))
		if err == nil {
			_ = l.Close()
			continue
		}
		if p.required {
			return nil, fmt.Errorf("%s %d is already in use by another program (%v). Stop that program first; "+
				"another BCH2 node or pool on this machine is the usual cause", p.what, p.port, err)
		}
		left = append(left, p.port)
	}
	return left, nil
}

// leftOutNote says what it means that another program holds the optional public port port, and
// what to do, as the Windows launcher and the dashboard say it.
func leftOutNote(port int) string {
	return fmt.Sprintf("another program uses port %d, the rental port: rentals have no port of their own until you "+
		"stop it and restart Forge Solo", port)
}

// noRentals is what the banner and the log put beside the miners' port while rentals have none.
func noRentals(port int) string {
	return fmt.Sprintf("(no rentals: another program uses port %d)", port)
}

// restrictDatabase makes a database an earlier run created readable by this user only (SQLite
// creates its files with the process's umask, which was 022 before this launcher set 077).
func restrictDatabase(dataDir string) {
	for _, f := range []string{"forgesolo.db", "forgesolo.db-wal", "forgesolo.db-shm"} {
		_ = os.Chmod(filepath.Join(dataDir, f), 0o600)
	}
}

// runOptions are forge-solo run's flags.
type runOptions struct {
	dataDir, web string
	reindex      bool // the node's first start rebuilds its chain state (-reindex)
}

func parseRunFlags(args []string) (runOptions, error) {
	var o runOptions
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.Usage = func() { usage(fs.Output()) }
	fs.StringVar(&o.dataDir, "data-dir", defaultDataDir(), "")
	fs.StringVar(&o.web, "web", defaultWeb, "")
	fs.BoolVar(&o.reindex, "reindex", false, "")
	if err := fs.Parse(args); err != nil {
		return runOptions{}, err
	}
	if fs.NArg() > 0 {
		return runOptions{}, fmt.Errorf("unexpected argument %q (see forge-solo help)", fs.Arg(0))
	}
	if err := checkWebAddr(o.web); err != nil {
		return runOptions{}, err
	}
	var err error
	o.dataDir, err = filepath.Abs(o.dataDir)
	return o, err
}

// geteuid is os.Geteuid; a test stands in root.
var geteuid = os.Geteuid

// errRootForeignDataDir: a run as root on a data directory another account owns.
var errRootForeignDataDir = errors.New("refusing to run as root on a data directory another account owns")

// checkRootDataDir refuses a run as root on a data directory that belongs to another account,
// most often the service's /var/lib/forge-solo. Everything such a run writes there would be
// root's -- the node's database files among them -- and that account's Forge Solo could no
// longer open them: the service's node then stopped at every start while systemd showed it
// running.
func checkRootDataDir(euid int, dataDir string) error {
	return checkRootDataDirAt(euid, dataDir, serviceData, unitPath)
}

// checkRootDataDirAt is checkRootDataDir with the service's data directory and unit file where
// they are given (the tests' own).
func checkRootDataDirAt(euid int, dataDir, svcData, unit string) error {
	if euid != 0 {
		return nil
	}
	st, err := os.Stat(dataDir)
	if err != nil {
		return nil // a new directory is root's own
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || sys.Uid == 0 {
		return nil
	}
	_, err = os.Stat(unit)
	exe := "forge-solo"
	if inst, err := installDir(); err == nil {
		exe = filepath.Join(inst, "forge-solo")
	}
	return rootRefusal(rootOwned{dir: dataDir, uid: sys.Uid, gid: sys.Gid, name: ownerName(sys.Uid),
		service: filepath.Clean(dataDir) == filepath.Clean(svcData), unit: err == nil, exe: exe, sudoUser: os.Getenv("SUDO_USER")})
}

// rootOwned is a data directory a run as root was refused, and what the refusal says to do.
type rootOwned struct {
	dir      string
	uid, gid uint32
	name     string // the owner's account: "" when the uid has none on this machine
	service  bool   // it is the service's data directory
	unit     bool   // the service is installed
	exe      string // this program
	sudoUser string // who ran sudo, from SUDO_USER
}

// rootRefusal says why a run as root on d is refused, and gives commands that work: sudo cannot
// run anything as a uid with no account, and chown takes "uid:" only for one with an account.
func rootRefusal(d rootOwned) error {
	owner := d.name
	if owner == "" {
		owner = fmt.Sprintf("uid %d, which has no account on this machine", d.uid)
	}
	why := fmt.Sprintf("%s belongs to %s. Files a run as root wrote there would be root's, and Forge Solo running as "+
		"its owner could no longer open them", d.dir, owner)
	you := d.sudoUser
	if you == "" || you == "root" {
		you = "YOUR-ACCOUNT"
	}
	makeYours := fmt.Sprintf("  To run it as your own account instead, without sudo, first make it yours: sudo chown -R %s: %s", you, d.dir)
	installed := filepath.Join(serviceDir, "forge-solo")
	var hints []string
	switch {
	case d.service && d.unit:
		hints = append(hints, "  It is the service's: start the service instead: sudo systemctl start "+serviceName)
		if d.name != "" {
			hints = append(hints, fmt.Sprintf("  To run it in a terminal, run it as the service's user: sudo -u %s %s run --data-dir %s", d.name, installed, d.dir))
		}
		hints = append(hints, fmt.Sprintf("  If a run as root has already left files there, sudo %s install-service gives them back to the service", installed))
	case d.service:
		hints = append(hints, fmt.Sprintf("  It is the data of the Forge Solo service, which is not installed now. To install it again, "+
			"which takes this data over: sudo %s install-service", d.exe))
		if d.name != "" {
			hints = append(hints, fmt.Sprintf("  To run it in a terminal as its owner: sudo -u %s %s run --data-dir %s", d.name, installed, d.dir))
		} else {
			hints = append(hints, makeYours)
		}
	case d.name != "":
		hints = append(hints, fmt.Sprintf("  Run it as its owner instead: sudo -u %s %s run --data-dir %s", d.name, d.exe, d.dir),
			fmt.Sprintf("  If a run as root has already left files there: sudo chown -R %s: %s", d.name, d.dir))
	default:
		hints = append(hints, makeYours,
			fmt.Sprintf("  If a run as root has already left files there: sudo chown -R %d:%d %s", d.uid, d.gid, d.dir))
	}
	return fmt.Errorf("%w: %s.\n%s", errRootForeignDataDir, why, strings.Join(hints, "\n"))
}

// lookupName is uid's account name as Forge Solo reads it (/etc/passwd only), and idName as the
// system finds it (LDAP, SSSD and systemd-homed too), the way sudo and chown do. Variables: the
// tests stand in others.
var (
	lookupName = func(uid string) (string, error) {
		u, err := user.LookupId(uid)
		if err != nil {
			return "", err
		}
		return u.Username, nil
	}
	idName = func(uid string) (string, error) {
		out, err := exec.Command("id", "-nu", uid).Output()
		return strings.TrimSpace(string(out)), err
	}
)

// ownerName is uid's account name: "" when it has none on this machine.
func ownerName(uid uint32) string {
	s := strconv.FormatUint(uint64(uid), 10)
	if n, err := lookupName(s); err == nil && n != "" {
		return n
	}
	if n, err := idName(s); err == nil && n != "" {
		return n
	}
	return ""
}

// rootWarning is shown when Forge Solo is started as root.
const rootWarning = "Warning: Forge Solo is running as root. It needs no root, and its mining service and node take " +
	"connections from the internet: run it as an ordinary user, or install the service " +
	"(sudo ./forge-solo install-service), which runs it as the forge-solo user."

// nodeChild is the BCH2 node. With reindex its first start rebuilds the chain state from the
// blocks on disk; a later start in the same run must not begin that again, and need not: the
// node itself carries on with a rebuild that was cut short. A node that stops saying its chain data
// is damaged is started once with -reindex, unless this run has already started it so.
func nodeChild(inst, dataDir, logDir string, reindex bool) *child {
	chain, out := filepath.Join(dataDir, "bch2"), filepath.Join(logDir, "node.log")
	c := &child{name: "node", path: filepath.Join(inst, "bin", "bitcoincashIId"), dir: dataDir, grace: nodeGrace,
		args: []string{"-datadir=" + chain, "-conf=" + filepath.Join(chain, "bch2.conf")},
		env:  os.Environ(), log: newRotatingLog(out, logMax),
		rebuild: &rebuild{chainDir: chain, logs: []string{filepath.Join(chain, "debug.log"), out}, done: reindex}}
	if reindex {
		c.onceArgs = []string{"-reindex"}
	}
	return c
}

// Stop allowances. The stratum gives its miners 2 s (both ports at once), waits for shares and a
// block still being processed or submitted (up to 15 s, only when there is one), and hands Forge
// Pool the TIDES shares it still holds (5 s per request). The node flushes its chain state to disk.
const (
	stratumGrace = 30 * time.Second
	apiGrace     = 10 * time.Second
	nodeGrace    = 120 * time.Second
	logMax       = 20 << 20
)

func runCmd(args []string) error {
	opts, err := parseRunFlags(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	dataDir, web := opts.dataDir, opts.web
	if err := checkKernel(getrandom(), kernelRelease()); err != nil {
		return err
	}
	if err := checkRootDataDir(geteuid(), dataDir); err != nil {
		return err
	}
	if geteuid() == 0 {
		fmt.Fprintln(os.Stderr, rootWarning)
	}
	inst, err := installDir()
	if err != nil {
		return err
	}
	if err := checkRelease(inst); err != nil {
		return err
	}
	// Everything this run and its programs create is for this user alone: the database holds the
	// settings and the TIDES gateway key. The programs inherit the mask.
	syscall.Umask(0o077)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	lock, err := lockDataDir(dataDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	left, err := checkPublicPorts()
	if err != nil {
		return err
	}
	for _, port := range left {
		logf("%s", leftOutNote(port))
	}
	rentals := len(left) == 0
	p, err := pickPorts()
	if err != nil {
		return err
	}
	sec, err := loadSecrets(dataDir)
	if err != nil {
		return fmt.Errorf("secrets.env: %w", err)
	}
	restrictDatabase(dataDir)
	if err := writeConfigs(dataDir, p, sec); err != nil {
		return err
	}
	password := ""
	if webNeedsPassword(web) {
		password = sec.DashboardPassword
	}
	webLn, err := net.Listen("tcp", web)
	if err != nil {
		return fmt.Errorf("the dashboard cannot listen on %s: %v (choose another with --web)", web, err)
	}

	logDir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return err
	}
	bin := func(n string) string { return filepath.Join(inst, "bin", n) }
	db := "DB_PATH=" + filepath.Join(dataDir, "forgesolo.db")
	rpcPort, apiPort, statsPort := strconv.Itoa(p.RPC), strconv.Itoa(p.API), strconv.Itoa(p.Stats)
	node := nodeChild(inst, dataDir, logDir, opts.reindex)
	api := &child{name: "api", path: bin("api"), dir: dataDir, grace: apiGrace,
		env: append(os.Environ(), apiEnv(dataDir, inst, p, sec)...),
		log: newRotatingLog(filepath.Join(logDir, "api.log"), logMax)}
	// INTERNAL_STATS_PORT is the stratum's own stats listener, which the API reads through
	// STRATUM_INTERNAL_URL.
	stratum := &child{name: "stratum", path: bin("stratum"), dir: dataDir, grace: stratumGrace,
		args: []string{"-config", filepath.Join(dataDir, "config.yaml")},
		env: append(os.Environ(), db,
			"INTERNAL_API_TOKEN="+sec.Token,
			"INTERNAL_STATS_HOST=127.0.0.1", "INTERNAL_STATS_PORT="+statsPort,
			"RPC_USER=forge", "RPC_PASSWORD="+sec.RPCPassword),
		log: newRotatingLog(filepath.Join(logDir, "stratum.log"), logMax)}
	all := []*child{stratum, api, node} // stop order

	// SIGHUP too (a closed terminal), unless it is ignored, as under nohup.
	sigCh := make(chan os.Signal, 2)
	sigs := []os.Signal{syscall.SIGINT, syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) {
		sigs = append(sigs, syscall.SIGHUP)
	}
	signal.Notify(sigCh, sigs...)

	logf("Forge Solo %s for Linux starting; data in %s", version, dataDir)
	if opts.reindex {
		logf("the node rebuilds its chain state from the blocks on disk (--reindex): this takes a while, and the dashboard shows it as syncing")
	}
	if err := node.Start(); err != nil {
		webLn.Close()
		return fmt.Errorf("the BCH2 node could not be started: %w", err)
	}
	if err := api.Start(); err != nil {
		node.Stop()
		webLn.Close()
		return fmt.Errorf("the dashboard API could not be started: %w", err)
	}
	srv := &http.Server{Handler: dashboardHandler(filepath.Join(inst, "web"), "127.0.0.1:"+apiPort, password),
		ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(webLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logf("the dashboard stopped serving: %v", err)
		}
	}()
	banner(os.Stdout, web, dataDir, password != "", rentals)

	// The stratum needs block templates: start it once the node's RPC answers. Until then the
	// dashboard shows the node's sync progress.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sayPayoutAddress(ctx, "http://127.0.0.1:"+apiPort)
	nodeUp := make(chan struct{})
	go func() {
		for ctx.Err() == nil {
			if c, err := net.DialTimeout("tcp", "127.0.0.1:"+rpcPort, time.Second); err == nil {
				c.Close()
				close(nodeUp)
				return
			}
			time.Sleep(time.Second)
		}
	}()
	var sig os.Signal
	select {
	case <-nodeUp:
		if err := stratum.Start(); err != nil {
			logf("the stratum could not be started: %v", err)
		} else if rentals {
			logf("mining service started: miners can connect to port %d (rentals %d)", stratumPort, rentalPort)
		} else {
			logf("mining service started: miners can connect to port %d %s", stratumPort, noRentals(rentalPort))
		}
		sig = <-sigCh
	case sig = <-sigCh:
	}
	cancel()

	logf("%v received: stopping cleanly (the node flushes its chain state; Ctrl-C again to kill at once)", sig)
	stopped := make(chan struct{})
	go func() {
		for _, c := range all {
			c.Stop()
			logf("%s stopped", c.name)
		}
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-sigCh:
		logf("killing everything now")
		for _, c := range all {
			c.Kill()
		}
		<-stopped
	}
	sctx, scancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = srv.Shutdown(sctx)
	scancel()
	for _, c := range all {
		_ = c.log.Close()
	}
	logf("Forge Solo stopped")
	return nil
}

// apiEnv is what the API is started with, besides this process's environment.
func apiEnv(dataDir, inst string, p ports, sec secrets) []string {
	rpcPort, apiPort, statsPort := strconv.Itoa(p.RPC), strconv.Itoa(p.API), strconv.Itoa(p.Stats)
	return []string{"DB_PATH=" + filepath.Join(dataDir, "forgesolo.db"),
		"RPC_URL=http://127.0.0.1:" + rpcPort, "RPC_USER=forge", "RPC_PASSWORD=" + sec.RPCPassword,
		"STRATUM_INTERNAL_URL=http://127.0.0.1:" + statsPort, "INTERNAL_API_TOKEN=" + sec.Token,
		"API_LISTEN_HOST=127.0.0.1", "API_LISTEN_PORT=" + apiPort,
		"MERGE_MINING_AVAILABLE=0", "WEB_ROOT=" + filepath.Join(inst, "web"),
		// Other accounts on this machine can reach the API, so a settings change needs a password:
		// the same DASHBOARD_PASSWORD the dashboard asks for when it listens beyond this machine.
		"SETTINGS_PASSWORD=" + sec.DashboardPassword, "FORGE_PLATFORM=linux"}
}

// banner is what forge-solo run prints once the dashboard serves. rentals is false when another
// program holds the rental port.
func banner(w io.Writer, web, dataDir string, password, rentals bool) {
	rent := fmt.Sprintf("(NiceHash / MiningRigRentals: %d)", rentalPort)
	if !rentals {
		rent = noRentals(rentalPort)
	}
	fmt.Fprintf(w, `
  Dashboard:  %s
  Miners:     stratum+tcp://<this machine's address>:%d   %s
  Data:       %s   (logs in logs/, the node's in bch2/debug.log)
`, dashboardURL(web), stratumPort, rent, dataDir)
	if password {
		fmt.Fprintf(w, "  Password:   user forge, DASHBOARD_PASSWORD in %s\n", filepath.Join(dataDir, "secrets.env"))
	}
	fmt.Fprintf(w, "  Settings:   saving a change asks for DASHBOARD_PASSWORD in %s\n", filepath.Join(dataDir, "secrets.env"))
	fmt.Fprintln(w)
}

// cliCmd runs bitcoincashII-cli against this install's node, with its config and credentials.
func cliCmd(args []string) error {
	dataDir := defaultDataDir()
	if len(args) >= 2 && args[0] == "--data-dir" {
		dataDir, args = args[1], args[2:]
	}
	inst, err := installDir()
	if err != nil {
		return err
	}
	code, err := cliRun{inst: inst, dataDir: dataDir, svcData: serviceData, unit: unitPath, args: args,
		stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}.run()
	if err == nil && code != 0 {
		os.Exit(code)
	}
	return err
}

// cliRun is one forge-solo cli: the release, the data directory, the command, where the service
// keeps its data and its unit (the tests' own), and the terminal.
type cliRun struct {
	inst, dataDir, svcData, unit string
	args                         []string
	stdin                        io.Reader
	stdout, stderr               io.Writer
}

// run runs the command against the node of the Forge Solo that runs with r.dataDir, and returns
// bitcoincashII-cli's exit code. A user who had run a copy of their own before installing the
// service got "Authorization failed" from the service's node, which their own copy's settings
// reached on the same port.
func (r cliRun) run() (int, error) {
	conf := filepath.Join(r.dataDir, "bch2", "bch2.conf")
	if _, err := os.Stat(conf); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return 0, fmt.Errorf("%s cannot be read as this user%s", conf, r.serviceHint(true))
		}
		return 0, fmt.Errorf("%s not found: is Forge Solo running with --data-dir %s?%s", conf, r.dataDir, r.serviceHint(false))
	}
	if !dataDirInUse(r.dataDir) {
		return 0, fmt.Errorf("no Forge Solo is running with the data directory %s: start it first, or give the one it "+
			"runs with (--data-dir)%s", r.dataDir, r.serviceHint(false))
	}
	var said bytes.Buffer
	cmd := exec.Command(filepath.Join(r.inst, "bin", "bitcoincashII-cli"),
		append([]string{"-datadir=" + filepath.Join(r.dataDir, "bch2"), "-conf=" + conf}, r.args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = r.stdin, r.stdout, io.MultiWriter(r.stderr, &said)
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return 0, err
	}
	if s := said.String(); strings.Contains(s, "Authorization failed") || strings.Contains(s, "Could not connect") {
		if hint := r.serviceHint(false); hint != "" {
			fmt.Fprintf(r.stderr, "forge-solo: that is the node of the Forge Solo running with %s.%s\n", r.dataDir, hint)
		}
	}
	return ee.ExitCode(), nil
}

// serviceHint says how to reach the service's node, when the service is installed and is not the
// one asked; always when this user cannot read the data directory.
func (r cliRun) serviceHint(always bool) string {
	if _, err := os.Stat(r.unit); err != nil || (filepath.Clean(r.dataDir) == filepath.Clean(r.svcData) && !always) {
		return ""
	}
	return fmt.Sprintf("\nFor the service's node, run cli as root: sudo %s cli %s",
		filepath.Join(serviceDir, "forge-solo"), strings.Join(r.args, " "))
}

// dataDirInUse reports whether a Forge Solo runs with dataDir: it holds forge-solo.lock there.
func dataDirInUse(dataDir string) bool {
	f, err := os.Open(filepath.Join(dataDir, "forge-solo.lock"))
	if err != nil {
		return !errors.Is(err, os.ErrNotExist)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return true
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}
