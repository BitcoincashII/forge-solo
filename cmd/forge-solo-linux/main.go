// Forge Solo for Linux runs the BCH2 node, the Forge Solo stratum and the dashboard API from one
// directory as supervised programs, and serves the dashboard. It is BCH2 only: there is no 1175
// node, so 1175 merge-mining is off and the dashboard leaves it out.
//
// A release directory holds this launcher, bin/ (bitcoincashIId, bitcoincashII-cli, stratum, api)
// and web/. Everything it writes goes to the data directory: the chain, the database (settings,
// blocks), secrets.env and logs/.
package main

import (
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
	"syscall"
	"time"
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
      Run it again from a newer release to upgrade; the data is kept.
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
  --reindex         rebuild the node's chain state from the blocks on disk, at this start only: for
                    a node that stops with "Corrupted block database detected"

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

// leftOutNote says what it means that another program holds the optional public port port.
func leftOutNote(port int) string {
	return fmt.Sprintf("another program uses port %d, the rental port: Forge Solo runs without it, so NiceHash and "+
		"MiningRigRentals cannot connect. Stop that program, then restart Forge Solo", port)
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
	owner := strconv.FormatUint(uint64(sys.Uid), 10)
	if u, err := user.LookupId(owner); err == nil {
		owner = u.Username
	}
	why := fmt.Sprintf("%s belongs to %s. Files a run as root wrote there would be root's, and Forge Solo running as %s "+
		"could no longer open them", dataDir, owner, owner)
	if filepath.Clean(dataDir) == serviceData {
		installed := filepath.Join(serviceDir, "forge-solo")
		return fmt.Errorf("%w: %s.\n"+
			"  It is the service's: start the service instead: sudo systemctl start %s\n"+
			"  To run it in a terminal, run it as the service's user: sudo -u %s %s run --data-dir %s\n"+
			"  If a run as root has already left files there, sudo %s install-service gives them back to the service",
			errRootForeignDataDir, why, serviceName, owner, installed, dataDir, installed)
	}
	exe := "forge-solo"
	if inst, err := installDir(); err == nil {
		exe = filepath.Join(inst, "forge-solo")
	}
	return fmt.Errorf("%w: %s.\n"+
		"  Run it as its owner instead: sudo -u %s %s run --data-dir %s\n"+
		"  If a run as root has already left files there: sudo chown -R %s: %s",
		errRootForeignDataDir, why, owner, exe, dataDir, owner, dataDir)
}

// rootWarning is shown when Forge Solo is started as root.
const rootWarning = "Warning: Forge Solo is running as root. It needs no root, and its mining service and node take " +
	"connections from the internet: run it as an ordinary user, or install the service " +
	"(sudo ./forge-solo install-service), which runs it as the forge-solo user."

// nodeChild is the BCH2 node. With reindex its first start rebuilds the chain state from the
// blocks on disk; a later start in the same run must not begin that again, and need not: the
// node itself carries on with a rebuild that was cut short.
func nodeChild(inst, dataDir, logDir string, reindex bool) *child {
	c := &child{name: "node", path: filepath.Join(inst, "bin", "bitcoincashIId"), dir: dataDir, grace: nodeGrace,
		args: []string{"-datadir=" + filepath.Join(dataDir, "bch2"), "-conf=" + filepath.Join(dataDir, "bch2", "bch2.conf")},
		env:  os.Environ(), log: newRotatingLog(filepath.Join(logDir, "node.log"), logMax)}
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
	// API_PORT points the stratum at the API for miner settings; INTERNAL_STATS_PORT is the
	// stratum's own stats listener, which the API reads through STRATUM_INTERNAL_URL.
	stratum := &child{name: "stratum", path: bin("stratum"), dir: dataDir, grace: stratumGrace,
		args: []string{"-config", filepath.Join(dataDir, "config.yaml")},
		env: append(os.Environ(), db,
			"INTERNAL_API_TOKEN="+sec.Token, "API_HOST=127.0.0.1", "API_PORT="+apiPort,
			"INTERNAL_STATS_HOST=127.0.0.1", "INTERNAL_STATS_PORT="+statsPort,
			"RPC_USER=forge", "RPC_PASSWORD="+sec.RPCPassword, "HOME_APP=1"),
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
			logf("mining service started: miners can connect to port %d (no rentals: another program has port %d)", stratumPort, rentalPort)
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
		"API_HOST=127.0.0.1", "API_PORT=" + apiPort, "API_LISTEN_HOST=127.0.0.1", "API_LISTEN_PORT=" + apiPort,
		"HOME_APP=1", "CORS_ORIGINS=", "MERGE_MINING_AVAILABLE=0", "WEB_ROOT=" + filepath.Join(inst, "web"),
		// Other accounts on this machine can reach the API, so a settings change needs a password:
		// the same DASHBOARD_PASSWORD the dashboard asks for when it listens beyond this machine.
		"SETTINGS_PASSWORD=" + sec.DashboardPassword, "FORGE_PLATFORM=linux"}
}

// banner is what forge-solo run prints once the dashboard serves. rentals is false when another
// program holds the rental port.
func banner(w io.Writer, web, dataDir string, password, rentals bool) {
	rent := fmt.Sprintf("(NiceHash / MiningRigRentals: %d)", rentalPort)
	if !rentals {
		rent = fmt.Sprintf("(no rentals: another program has port %d)", rentalPort)
	}
	fmt.Fprintf(w, `
  Dashboard:  http://%s
  Miners:     stratum+tcp://<this machine's address>:%d   %s
  Data:       %s   (logs in logs/, the node's in bch2/debug.log)
`, web, stratumPort, rent, dataDir)
	if password {
		fmt.Fprintf(w, "  Password:   user forge, DASHBOARD_PASSWORD in %s\n", filepath.Join(dataDir, "secrets.env"))
	}
	fmt.Fprintf(w, "  Settings:   saving a change asks for DASHBOARD_PASSWORD in %s\n", filepath.Join(dataDir, "secrets.env"))
	fmt.Fprintln(w, "  Set your BCH2 payout address in the dashboard's Settings: mining waits for it.")
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
	conf := filepath.Join(dataDir, "bch2", "bch2.conf")
	if _, err := os.Stat(conf); err != nil {
		return fmt.Errorf("%s not found: is Forge Solo running with --data-dir %s? (as the service: sudo -u %s forge-solo cli --data-dir %s …)",
			conf, dataDir, serviceUser, serviceData)
	}
	cmd := exec.Command(filepath.Join(inst, "bin", "bitcoincashII-cli"),
		append([]string{"-datadir=" + filepath.Join(dataDir, "bch2"), "-conf=" + conf}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}
