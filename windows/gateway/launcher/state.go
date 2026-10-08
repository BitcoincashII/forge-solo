package main

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// statePollEvery is how often the tray asks the gateway what it is doing, so that its tooltip
// follows the status page (shorter in the tests).
var statePollEvery = 10 * time.Second

// stateAskTimeout is how long one question to the gateway may take; the watch asks again later.
var stateAskTimeout = 3 * time.Second

// stateTips is what the tray says for each state the gateway's /api/status gives. A pool that
// cannot be reached, or that refuses this PC's clock, is said as the gateway's mode has it: mining
// solo meanwhile, or, with pool only on, miners turned away (tipForState).
var stateTips = map[string]string{
	"unconfigured":     tipSetUp,
	"node_unreachable": tipNodeUnreachable,
	"node_login":       tipNodeLogin,
	"node_forbidden":   tipNodeForbidden,
	"node_syncing":     tipNodeSyncing,
	"pool_unreachable": tipPoolSolo,
	"clock_off":        tipClockSolo,
	"starting":         tipWaitingForWork,
	"active":           tipActive,
}

// A gatewayAnswer is what the gateway's /api/status said: whether it is set up, its state and its
// mode.
type gatewayAnswer struct {
	configured  bool
	state, mode string
}

// gatewayStateSaid asks the gateway what it is doing (a stand-in in the tests).
var gatewayStateSaid = askGatewayState

// askGatewayState asks the gateway's status page what it is doing. known is whether it could say:
// it answered, with a state.
func askGatewayState() (a gatewayAnswer, known bool) {
	c := &http.Client{Timeout: stateAskTimeout}
	resp, err := c.Get(statusURL() + "api/status")
	if err != nil {
		return a, false
	}
	defer resp.Body.Close()
	var s struct {
		Configured *bool  `json:"configured"`
		State      string `json:"state"`
		Mode       string `json:"mode"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&s) != nil || s.State == "" {
		return a, false
	}
	a = gatewayAnswer{configured: s.State != "unconfigured", state: s.State, mode: s.Mode}
	if s.Configured != nil {
		a.configured = *s.Configured
	}
	return a, true
}

// The gateway's last answer. answered is whether there has been one, which Open Status Page goes
// by; current whether it came from the gateway running now, which the tooltip goes by.
var (
	stateMu  sync.Mutex
	answer   gatewayAnswer
	answered bool
	current  bool
)

// noteState keeps what the gateway said, and when its state or mode changed, logs it and updates
// the tray.
func noteState(a gatewayAnswer) {
	stateMu.Lock()
	was, had := answer, current
	answer, answered, current = a, true, true
	stateMu.Unlock()
	if had && was.state == a.state && was.mode == a.mode {
		return
	}
	if !had || was.state != a.state {
		logf("the gateway says: %s", a.state)
	}
	showRunning()
}

// forgetState forgets the state of a gateway that stopped: the one started next says its own. What
// it said of its settings is kept for Open Status Page.
func forgetState() {
	stateMu.Lock()
	current = false
	stateMu.Unlock()
}

// lastAnswer is the gateway's last answer, and whether there has been one.
func lastAnswer() (gatewayAnswer, bool) {
	stateMu.Lock()
	defer stateMu.Unlock()
	return answer, answered
}

// currentState is the last answer of the gateway running now, and whether it has given one.
func currentState() (gatewayAnswer, bool) {
	stateMu.Lock()
	defer stateMu.Unlock()
	return answer, current
}

// stateWatching is set while the watch of the gateway's state runs, which stateWatch waits for.
var (
	stateWatching atomic.Bool
	stateWatch    sync.WaitGroup
)

// watchGatewayState starts, once, the watch of the gateway's state: it is asked at once, and then
// every statePollEvery until the stop begins, and the tray follows what it says. It runs on its
// own, so that neither the start nor the tray menu waits on the gateway. An answer it could not
// get changes nothing.
func watchGatewayState() {
	if !stateWatching.CompareAndSwap(false, true) {
		return
	}
	stateWatch.Add(1)
	go func() {
		defer stateWatch.Done()
		defer stateWatching.Store(false)
		for {
			if a, known := gatewayStateSaid(); known {
				noteState(a)
			}
			if pause(statePollEvery) {
				return
			}
		}
	}()
}
