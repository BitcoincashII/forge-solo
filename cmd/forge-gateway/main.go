// Command forge-gateway mines into Forge Pool's TIDES window from your own BCH2 node, for miners
// who do not run Forge Solo -- the same thing Forge Solo's TIDES mode does, on its own.
//
// Your node builds every block template; your miners connect to this program's stratum port;
// the pool registers each job and credits the shares your miners find, and every block found
// through the pool's DATUM gateways pays its TIDES split straight from the coinbase, with no pool
// fee. It is a DATUM-style gateway: DATUM and TIDES were designed by OCEAN (see LICENSE).
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	"github.com/BitcoincashII/forge-solo/internal/datum/gateway"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "1.0.0-dev"

func usage() {
	fmt.Fprintf(os.Stderr, `Forge Gateway %s: mine into Forge Pool's TIDES window from your own BCH2 node.

Usage:
  forge-gateway [-config forge-gateway.json]   run
  forge-gateway -check [-config ...]           check the config, the node login and the pool, then exit
  forge-gateway -version
`, version)
	if serviceSupported {
		fmt.Fprintf(os.Stderr, `  forge-gateway install -config C:\path\forge-gateway.json   run as a Windows service, at startup
  forge-gateway uninstall                                        remove the Windows service
`)
	}
	fmt.Fprintf(os.Stderr, `
With SETTINGS_PASSWORD set (16 characters or more), the status page's Settings can change the node, the payout address, the coinbase tag and pool_only. The Windows tray app sets it.
`)
}

func main() {
	if len(os.Args) > 1 && serviceSupported {
		switch os.Args[1] {
		case "install", "uninstall":
			os.Exit(serviceCommand(os.Args[1], os.Args[2:]))
		}
	}
	fs := flag.NewFlagSet("forge-gateway", flag.ExitOnError)
	fs.Usage = usage
	cfgPath := fs.String("config", "forge-gateway.json", "path to the config file")
	check := fs.Bool("check", false, "check the config, the node login and the pool, then exit")
	showVersion := fs.Bool("version", false, "print the version and exit")
	fs.Parse(os.Args[1:])

	if *showVersion {
		fmt.Println("forge-gateway", version)
		return
	}
	if *check {
		os.Exit(runCheck(*cfgPath))
	}
	if isWindowsService() {
		if err := runService(*cfgPath); err != nil {
			os.Exit(1)
		}
		return
	}
	stop := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	// The Windows tray app cannot send this program a signal: it asks for the same clean stop by
	// closing its stdin. Opt-in: a stdin that is /dev/null ends at once.
	if os.Getenv("FORGE_STOP_ON_STDIN_EOF") == "1" {
		go stopOnEOF(os.Stdin, sig)
	}
	go func() {
		<-sig
		close(stop)
	}()
	if err := run(*cfgPath, stop, false); err != nil {
		fmt.Fprintln(os.Stderr, "forge-gateway:", err)
		os.Exit(exitCode(err))
	}
}

// settingsPasswordFromEnv is SETTINGS_PASSWORD, which turns the status page's Settings on: "" when
// it is not set.
func settingsPasswordFromEnv() (string, error) {
	pw := strings.TrimSpace(os.Getenv("SETTINGS_PASSWORD"))
	if pw != "" && utf8.RuneCountInString(pw) < 16 {
		return "", errors.New("SETTINGS_PASSWORD is shorter than 16 characters: use a long random one (the Windows tray app makes 64 hex characters)")
	}
	return pw, nil
}

// run is the gateway until stop closes. A service logs to a file next to its config unless the
// config names one: it has no console.
//
// Started with SETTINGS_PASSWORD (the Windows tray app does that), the status page's Settings can
// change the node, the payout address, the coinbase tag and pool_only, and the gateway runs until
// they are right: a config that lacks them, a node that refuses the login or does not answer are
// shown on the status page, never a reason to exit. Without it (the console, the service) the
// start is 1.0.0's.
func run(cfgPath string, stop <-chan struct{}, asService bool) error {
	password := ""
	if !asService {
		pw, err := settingsPasswordFromEnv()
		if err != nil {
			return &exitError{exitConfig, err}
		}
		password = pw
	}
	var cfg *Config
	var err error
	problem := ""
	if password == "" {
		if cfg, err = loadConfig(cfgPath); err != nil {
			return &exitError{exitConfig, err}
		}
	} else {
		if cfg, err = readConfig(cfgPath); err != nil {
			return &exitError{exitConfig, err}
		}
		if cfg.Status.Listen == "off" {
			return &exitError{exitConfig, errors.New("status.listen is off, but Settings needs the status page: set it to 127.0.0.1:3090")}
		}
		problem = cfg.setupProblem()
	}
	if asService && cfg.LogFile == "" {
		cfg.LogFile = filepath.Join(cfg.dir, "forge-gateway.log")
	}
	log, err := newLogger(cfg.LogFile, cfg.LogLevel)
	if err != nil {
		return err
	}
	log = wrapLog(log)
	defer log.Sync()
	// The job manager says what it does with the standard library's log, on stderr; its "no
	// payout address configured" (it is given the address after it is made) is not true here.
	// Only the gateway's own log speaks, and what that one says is kept at debug.
	if undo, err := zap.RedirectStdLogAt(log.Named("mining"), zapcore.DebugLevel); err == nil {
		defer undo()
	}
	log.Info("Forge Gateway starting", zap.String("version", version), zap.String("payout_address", cfg.Mining.PayoutAddress),
		zap.String("pool", cfg.Pool.URL), zap.String("node", cfg.Node.RPCURL), zap.String("stratum", cfg.Stratum.Listen),
		zap.Bool("pool_only", cfg.Mining.PoolOnly))

	key, created, err := loadOrCreateKey(cfg.Pool.KeyFile)
	if err != nil {
		return fmt.Errorf("gateway key: %w", err)
	}
	if created {
		log.Info("created this gateway's identity key; keep the file to keep the same identity at the pool",
			zap.String("key_file", cfg.Pool.KeyFile))
	}
	if password != "" {
		log.Info("the status page's Settings can change the node, the payout address, the coinbase tag and pool_only, with the settings password")
	} else if err := firstNodeCheck(log, cfg); err != nil {
		return err
	}

	// The app's own stop: the process stop, or a start that failed.
	quit := make(chan struct{})
	var quitOnce sync.Once
	end := func() { quitOnce.Do(func() { close(quit) }) }
	go func() {
		select {
		case <-stop:
			end()
		case <-quit:
		}
	}()
	a := newApp(log, cfgPath, password, key, cfg, quit)
	// Asked for before anything listens, so the status page's first answer already says whether
	// the gateway is set up.
	a.reload(cfg, problem)
	go a.logStates(quit)
	failed := func(err error) error {
		end()
		<-a.applierDone
		a.endEngine()
		a.srv.Stop()
		return &exitError{exitPort, err}
	}
	if err := a.srv.Start(); err != nil {
		return failed(fmt.Errorf("stratum %s: %w", cfg.Stratum.Listen, err))
	}
	if cfg.Status.Listen != "off" {
		if err := a.serveStatus(cfg.Status.Listen); err != nil {
			return failed(fmt.Errorf("status %s: %w", cfg.Status.Listen, err))
		}
		log.Info("status page", zap.String("url", "http://"+cfg.Status.Listen+"/"))
	}
	started(a)
	go a.gw.Run(stop)
	log.Info("⛏️  ready: point your miners here", zap.String("stratum", "stratum+tcp://"+cfg.Stratum.Listen),
		zap.String("gateway_id", a.gw.ID()))

	<-stop
	a.shutdown()
	return nil
}

// firstNodeCheck is the console's and the service's start, as 1.0.0's: a node login that cannot be
// read, or a node that refuses the login or this computer, ends the gateway; a node that does not
// answer yet does not.
func firstNodeCheck(log *zap.Logger, cfg *Config) error {
	user, pass, err := cfg.rpcLogin()
	if err != nil {
		return err
	}
	ci, err := newNode(cfg.Node.RPCURL, user, pass).chainInfo()
	if err != nil {
		if errors.Is(err, errUnauthorized) || errors.Is(err, errForbidden) {
			return err
		}
		log.Warn("the node is not answering yet; the gateway keeps trying", zap.Error(err))
		return nil
	}
	log.Info("node", zap.String("chain", ci.Chain), zap.Int64("blocks", ci.Blocks), zap.Int64("headers", ci.Headers),
		zap.Bool("syncing", ci.InitialBlockDownload))
	if ci.Chain != "main" {
		log.Warn("the node is not on mainnet; Forge Pool takes mainnet work only, so this gateway will mine solo", zap.String("chain", ci.Chain))
	}
	return nil
}

// started is told of the running gateway once it listens, and wrapLog may add to its log: the
// tests' way in.
var (
	started = func(*app) {}
	wrapLog = func(l *zap.Logger) *zap.Logger { return l }
)

func newLogger(file, level string) (*zap.Logger, error) {
	var lvl zapcore.Level
	if err := lvl.Set(level); err != nil {
		return nil, err
	}
	enc := zap.NewProductionEncoderConfig()
	enc.EncodeTime = zapcore.ISO8601TimeEncoder
	enc.EncodeLevel = zapcore.CapitalLevelEncoder
	out := zapcore.Lock(os.Stdout)
	if file != "" {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return nil, err
		}
		f, err := openRotating(file)
		if err != nil {
			return nil, err
		}
		out = zapcore.Lock(f)
	}
	return zap.New(zapcore.NewCore(zapcore.NewConsoleEncoder(enc), out, lvl)), nil
}

// runCheck is -check: every problem a user can fix before leaving the gateway running.
func runCheck(cfgPath string) int {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		fmt.Println("✗ config:", err)
		return 1
	}
	fmt.Println("✓ config", cfgPath)
	fmt.Println("  payout address:", cfg.Mining.PayoutAddress)
	ok := true
	user, pass, err := cfg.rpcLogin()
	if err != nil {
		fmt.Println("✗ node login:", err)
		return 1
	}
	if ci, err := newNode(cfg.Node.RPCURL, user, pass).chainInfo(); err != nil {
		fmt.Println("✗ node", cfg.Node.RPCURL+":", err)
		ok = false
	} else {
		fmt.Printf("✓ node %s: chain %s, block %d of %d headers", cfg.Node.RPCURL, ci.Chain, ci.Blocks, ci.Headers)
		if ci.InitialBlockDownload {
			fmt.Print(" (still syncing: no work until it is done)")
		}
		fmt.Println()
		if ci.Chain != "main" {
			fmt.Println("  ! not mainnet: Forge Pool takes mainnet work only")
		}
	}
	key, _, err := loadOrCreateKey(cfg.Pool.KeyFile)
	if err != nil {
		fmt.Println("✗ gateway key:", err)
		return 1
	}
	if snap, err := gateway.New(cfg.Pool.URL, key).Snapshot(); err != nil {
		fmt.Println("✗ pool", cfg.Pool.URL+":", err)
		ok = false
	} else {
		fmt.Printf("✓ pool %s: TIDES window at block %d, %d miners in it\n", cfg.Pool.URL, snap.Height, len(snap.Work))
	}
	if !ok {
		return 1
	}
	fmt.Println("All good: run forge-gateway without -check to start mining.")
	return 0
}
