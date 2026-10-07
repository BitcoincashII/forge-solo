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
	"syscall"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/datum/gateway"
	"github.com/BitcoincashII/forge-solo/internal/mining"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
	"github.com/BitcoincashII/forge-solo/internal/tidesgw"
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
	go func() {
		<-sig
		close(stop)
	}()
	if err := run(*cfgPath, stop, false); err != nil {
		fmt.Fprintln(os.Stderr, "forge-gateway:", err)
		os.Exit(1)
	}
}

// run is the gateway until stop closes. A service logs to a file next to its config unless the
// config names one: it has no console.
func run(cfgPath string, stop <-chan struct{}, asService bool) error {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	if asService && cfg.LogFile == "" {
		cfg.LogFile = filepath.Join(cfg.dir, "forge-gateway.log")
	}
	log, err := newLogger(cfg.LogFile, cfg.LogLevel)
	if err != nil {
		return err
	}
	defer log.Sync()
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
	user, pass, err := cfg.rpcLogin()
	if err != nil {
		return err
	}
	n := newNode(cfg.Node.RPCURL, user, pass)
	if ci, err := n.chainInfo(); err != nil {
		if errors.Is(err, errUnauthorized) {
			return err
		}
		log.Warn("the node is not answering yet; the gateway keeps trying", zap.Error(err))
	} else {
		log.Info("node", zap.String("chain", ci.Chain), zap.Int64("blocks", ci.Blocks), zap.Int64("headers", ci.Headers),
			zap.Bool("syncing", ci.InitialBlockDownload))
		if ci.Chain != "main" {
			log.Warn("the node is not on mainnet; Forge Pool takes mainnet work only, so this gateway will mine solo", zap.String("chain", ci.Chain))
		}
	}

	jm := mining.NewJobManager(cfg.Node.RPCURL, user, pass, cfg.Mining.PayoutAddress, cfg.Mining.CoinbaseTag)
	var srv *stratum.Server
	gw := tidesgw.New(tidesgw.Config{PoolURL: cfg.Pool.URL, Key: key, Logger: log.Named("pool"), PoolOnly: cfg.Mining.PoolOnly,
		// Each job commits to a share difficulty above the busiest miner's, so the pool credits
		// every share in full (srv is set before the job loop, which alone calls this, starts).
		MaxDifficulty: func() float64 { return srv.MaxDifficulty() }})
	hist := newJobHistory()
	proc := &processor{log: log, gw: gw, hist: hist, node: n, stats: newMinerStats()}
	host, port, _ := hostPort(cfg.Stratum.Listen)
	srv = stratum.NewServer(&stratum.ServerConfig{
		Host:                host,
		Port:                port,
		MaxConnections:      cfg.Stratum.MaxConnections,
		MaxConnectionsPerIP: cfg.Stratum.MaxConnectionsPerIP,
		MaxSharesPerSecond:  100,
		VardiffEnabled:      true,
		MinDiff:             cfg.Stratum.MinDifficulty,
		AbsoluteMinDiff:     cfg.Stratum.MinDifficulty,
		MaxDiff:             cfg.Stratum.MaxDifficulty,
		VariancePercent:     0.25,
		TargetShareTime:     cfg.Stratum.TargetShareSeconds,
		RetargetTime:        cfg.Stratum.RetargetSeconds,
		// The DATUM coinbase reserves 12 extranonce bytes: 4 per connection, 8 for the miner.
		ExtraNonce1Size: 4,
		ExtraNonce2Size: 8,
		ServerName:      "gateway",
		// Solo-style logins: a miner's username is its BCH2 address (credited to it at the pool)
		// or any worker name (credited to the payout address).
		SoloOnly: true,
	}, log.Named("stratum"), proc)
	srv.SetSoloPayoutAddress(cfg.Mining.PayoutAddress)
	loop := newJobLoop(log, jm, gw, srv, hist, cfg.Mining.PayoutAddress, cfg.Mining.PoolOnly)
	if err := srv.Start(); err != nil {
		return fmt.Errorf("stratum %s: %w", cfg.Stratum.Listen, err)
	}
	defer srv.Stop()
	go gw.Run(stop)
	go loop.run(stop)
	if cfg.Status.Listen != "off" {
		state := &gatewayState{cfg: cfg, started: time.Now(), gw: gw, loop: loop, srv: srv, proc: proc}
		if _, err := serveStatus(cfg.Status.Listen, state.statusHandler(), stop); err != nil {
			return fmt.Errorf("status %s: %w", cfg.Status.Listen, err)
		}
		log.Info("status page", zap.String("url", "http://"+cfg.Status.Listen+"/"))
	}
	log.Info("⛏️  ready: point your miners here", zap.String("stratum", "stratum+tcp://"+cfg.Stratum.Listen),
		zap.String("gateway_id", gw.ID()))

	<-stop
	// The miners first: Stop handles what they send in its grace and waits for shares, a block
	// among them, still being processed. Flushing before it (with Stop deferred) left the shares
	// accepted in the last seconds queued after the flush, and lost.
	log.Info("stopping: closing the miners' connections, then sending the pool the shares still queued")
	srv.Stop()
	gw.Flush()
	return nil
}

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
