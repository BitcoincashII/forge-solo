package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/BitcoincashII/forge-solo/internal/tidesgw"
)

// What the gateway is doing, for the status page and the Windows tray app.
const (
	stateUnconfigured    = "unconfigured"
	stateNodeUnreachable = "node_unreachable"
	stateNodeLogin       = "node_login"
	stateNodeForbidden   = "node_forbidden"
	stateNodeSyncing     = "node_syncing"
	statePoolUnreachable = "pool_unreachable"
	stateClockOff        = "clock_off"
	stateStarting        = "starting"
	stateActive          = "active"
)

// stateInput is everything the state is made of.
type stateInput struct {
	engine     bool   // an engine is stored
	problem    string // the stored engine's; before the first apply, the one asked for
	loginErr   error
	cookie     bool // cookie login
	cookiePath string
	rpcURL     string
	health     *healthResult // nil before the first check
	pool       tidesgw.Status
	mode       string // statusView.Mode
	poolOnly   bool
	windows    bool // the gateway runs on Windows: the clock's texts say where to set it there
}

const startingReason = "Waiting for the first block template from your node and the first job Forge Pool registers."

// stateOf is the state and the reason the status page gives for it. The first that applies wins:
// a setup problem before the node, the node before the pool.
func stateOf(in stateInput) (state, reason string) {
	h := in.health
	switch {
	case in.problem != "":
		return stateUnconfigured, in.problem
	case !in.engine:
		return stateStarting, "Forge Gateway is starting."
	case in.loginErr != nil:
		return stateNodeLogin, "Forge Gateway cannot read the node's cookie file " + in.cookiePath + ": " + cookieTrouble(in.loginErr) +
			". Check that the node is running and that the cookie file in Settings is the node's."
	case h != nil && h.unauthorized && !in.cookie:
		return stateNodeLogin, "Your node refused the RPC login. Check the RPC user and password in Settings: they must be the rpcuser and rpcpassword in the node's config file."
	case h != nil && h.unauthorized:
		return stateNodeLogin, "Your node refused the login from its cookie file. Check that the cookie file in Settings is the running node's."
	case h != nil && h.forbidden:
		return stateNodeForbidden, forbiddenReason(in.rpcURL)
	case h != nil && h.err != nil:
		return stateNodeUnreachable, "Forge Gateway cannot reach your node at " + in.rpcURL + ": " + briefNodeError(h.err) +
			". Check that the node is running with server=1 and that the RPC address in Settings is right."
	case h != nil && h.ci != nil && h.ci.InitialBlockDownload:
		return stateNodeSyncing, fmt.Sprintf("Your node is still syncing: block %d of %d. Mining starts when it is done.", h.ci.Blocks, h.ci.Headers)
	case in.pool.NodeBehind:
		then := "Your miners mine solo until your node is on Forge Pool's block."
		if in.poolOnly {
			then = "Miners are turned away until your node is on Forge Pool's block."
		}
		why := upperFirst(in.pool.Reason)
		if why == "" {
			why = "Your node is not on Forge Pool's block yet"
		}
		return stateNodeSyncing, why + ". " + then
	case (in.mode == "solo" || in.mode == "waiting") && poolRefusesClock(in.pool.Reason):
		off, _ := poolClockOff(in.pool.Reason)
		return stateClockOff, clockReason(off, in.windows, in.mode)
	case in.mode == "solo":
		return statePoolUnreachable, "Forge Pool cannot be reached" + inBrackets(in.pool.Reason) +
			". Your miners mine solo on your node meanwhile: a block found now pays your payout address in full. Forge Gateway tries the pool again every minute."
	case in.mode == "waiting":
		return statePoolUnreachable, "Forge Pool cannot be reached" + inBrackets(in.pool.Reason) +
			". Pool only is on, so miners are turned away until it is back, and fail over to their backup pool."
	case in.mode == "tides":
		return stateActive, "Mining into Forge Pool's TIDES window."
	}
	return stateStarting, startingReason
}

// forbiddenReason is why a node answers 403, whatever the login: it lets RPC in only from the
// addresses in its rpcallowip lines, and when it has none of them, only at an address given as a
// number (its guard against DNS rebinding refuses a name such as localhost).
func forbiddenReason(rpcURL string) string {
	why := "Your node refuses this computer (HTTP 403): it answers RPC only from the addresses in the rpcallowip lines of its config file. " +
		"For a node on another computer, add rpcallowip=<this computer's address> and rpcbind=<the node's address> to that file and restart the node."
	if u, err := url.Parse(rpcURL); err == nil && u.Hostname() != "" && net.ParseIP(u.Hostname()) == nil {
		why += " A node with no rpcallowip line also refuses an address given by name, such as " + u.Hostname() +
			": for a node on this computer, enter http://127.0.0.1:8342 in Settings."
	}
	return why
}

// nodeErrorShown is what the status page's node card says of the last node check that failed: in a
// few words, and with no key of the config file, which the tray app's user never sees.
func nodeErrorShown(err error, cookie bool) string {
	switch {
	case errors.Is(err, errForbidden):
		return "it refuses this computer (HTTP 403)"
	case errors.Is(err, errUnauthorized) && cookie:
		return "it refused the login from its cookie file"
	case errors.Is(err, errUnauthorized):
		return "it refused the login: check the RPC user and password in Settings"
	}
	return briefNodeError(err)
}

// Forge Pool refuses a signed request whose time is more than 2 minutes off its own clock: it
// answers 401 {"error":"request time is <how far> off the pool's clock"} (internal/datum/wire,
// Verify), and the pool gateway's reason carries that answer. The pool was reached: only setting
// this computer's clock right helps.
var poolClockText = regexp.MustCompile(`request time is (-?[0-9][0-9a-zµ.]*) off the pool's clock`)

// poolRefusesClock reports whether the pool gateway's reason is that refusal.
func poolRefusesClock(reason string) bool {
	return strings.Contains(reason, " 401 ") && strings.Contains(reason, "off the pool's clock")
}

// poolClockOff is how far off the pool found this computer's clock, from the pool gateway's reason;
// 0 when it does not say.
func poolClockOff(reason string) (time.Duration, bool) {
	if !poolRefusesClock(reason) {
		return 0, false
	}
	m := poolClockText.FindStringSubmatch(reason)
	if m == nil {
		return 0, true
	}
	d, err := time.ParseDuration(m[1])
	if err != nil {
		return 0, true
	}
	if d < 0 {
		d = -d
	}
	return d, true
}

// howFar is d in round words: about 7 hours, about 3 minutes, about 2 days; "" when it is not known.
func howFar(d time.Duration) string {
	about := func(n time.Duration, one, many string) string {
		if n <= 1 {
			return "about " + one
		}
		return fmt.Sprintf("about %d %s", int64(n), many)
	}
	switch {
	case d <= 0:
		return ""
	case d >= 36*time.Hour:
		return about(d.Round(24*time.Hour)/(24*time.Hour), "a day", "days")
	case d >= 50*time.Minute:
		return about(d.Round(time.Hour)/time.Hour, "an hour", "hours")
	}
	return about(d.Round(time.Minute)/time.Minute, "a minute", "minutes")
}

// clockWhose is whose clock is off, as the texts say it.
func clockWhose(windows bool) string {
	if windows {
		return "this PC's clock"
	}
	return "this computer's clock"
}

// clockShort is the Forge Pool card's reason while the pool refuses this computer's clock.
func clockShort(off time.Duration, windows bool) string {
	if far := howFar(off); far != "" {
		return clockWhose(windows) + " is " + far + " off"
	}
	return clockWhose(windows) + " is off"
}

// clockReason is the state's reason while the pool refuses this computer's clock: how far off it
// is, where to set it right, and what the miners do meanwhile.
func clockReason(off time.Duration, windows bool, mode string) string {
	fix := "set it right, for example by turning on network time (timedatectl set-ntp true)"
	if windows {
		fix = "turn on Set time automatically in Windows Settings, Time & language"
	}
	then := "Your miners mine solo on your node meanwhile: a block found now pays your payout address in full."
	if mode == "waiting" {
		then = "Pool only is on, so miners are turned away meanwhile, and fail over to their backup pool."
	}
	return upperFirst(clockShort(off, windows)) + ". Forge Pool refuses requests until it is right: " + fix + ". " + then +
		" Forge Gateway goes back to the pool by itself once the clock is right."
}

func upperFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

func inBrackets(s string) string {
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}

// cookieTrouble is why the cookie file could not be read, in a few words.
func cookieTrouble(err error) string {
	var nc *notCookieError
	var pe *fs.PathError
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "it does not exist"
	case errors.As(err, &nc):
		return "it does not hold one line, user:password"
	case errors.As(err, &pe):
		return pe.Err.Error()
	}
	return err.Error()
}

// stateNow is the gateway's state and its reason now, with whether it is set up and its mode.
func (a *app) stateNow() (configured bool, state, reason, mode string) {
	pool := a.gw.Status()
	e, cfg, problem := a.settingsNow()
	mode = a.mode(e, cfg, problem, pool)
	in := stateInput{engine: e != nil, problem: problem, pool: pool, mode: mode, poolOnly: cfg.Mining.PoolOnly,
		cookie: cfg.Node.RPCUser == "", cookiePath: cfg.Node.RPCCookieFile, rpcURL: cfg.Node.RPCURL, windows: runtime.GOOS == "windows"}
	if e != nil {
		in.loginErr = e.loginErr
		if e.health != nil {
			in.health = e.health.last.Load()
		}
	}
	state, reason = stateOf(in)
	return problem == "", state, reason, mode
}

// mode is what the miners are doing: "tides", "solo" (the pool cannot be reached), "waiting"
// (pool_only, the pool cannot be reached), "starting", or "off" (no job loop: not set up, or no
// node login).
func (a *app) mode(e *engine, cfg *Config, problem string, pool tidesgw.Status) string {
	var loop *jobLoop
	if e != nil {
		loop = e.loop.Load()
	}
	if loop == nil {
		if e != nil || problem != "" {
			return "off"
		}
		return "starting"
	}
	job := loop.current.Load()
	switch {
	case job == nil && pool.State == tidesgw.StateStarting:
		return "starting"
	case cfg.Mining.PoolOnly && !loop.door.Load():
		return "waiting"
	case job != nil && job.Tides:
		return "tides"
	}
	return "solo"
}

// stateLogEvery is how often the gateway looks whether its state changed, to log it.
var stateLogEvery = time.Second

// logStates logs each change of state once, until stop.
func (a *app) logStates(stop <-chan struct{}) {
	t := time.NewTicker(stateLogEvery)
	defer t.Stop()
	last := ""
	for {
		if _, state, reason, _ := a.stateNow(); state != last {
			last = state
			msg := "state: " + state + ": " + reason
			switch state {
			case stateUnconfigured, stateNodeUnreachable, stateNodeLogin, stateNodeForbidden, stateNodeSyncing, statePoolUnreachable, stateClockOff:
				a.log.Warn(msg)
			default:
				a.log.Info(msg)
			}
		}
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}
