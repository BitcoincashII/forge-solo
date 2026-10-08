package main

import (
	"errors"
	"net/http"
	"sync/atomic"
	"time"
)

var (
	// nodeCheckEvery is how often the gateway asks its node how it is.
	nodeCheckEvery = 10 * time.Second
	// nodeCheckTimeout bounds one check: a node that takes the connection and never answers shows
	// as unreachable within one check, not after the 30 s a block submit may wait.
	nodeCheckTimeout = 5 * time.Second
)

// healthResult is one check of the node.
type healthResult struct {
	at           time.Time
	ci           *chainInfo // nil when the check failed
	err          error
	unauthorized bool // the node refused the login
}

// nodeHealth checks an engine's node every nodeCheckEvery. With a cookie login it follows the node:
// a node that restarts writes a new cookie, and one that is stopped has none, so the cookie file is
// read again when the node refuses the login, or until it can be read.
type nodeHealth struct {
	a     *app
	e     *engine
	check *node // nil while the cookie file cannot be read (e.loginErr)
	last  atomic.Pointer[healthResult]
	good  atomic.Pointer[chainInfo] // the last check that worked
}

func newNodeHealth(a *app, e *engine, user, pass string) *nodeHealth {
	h := &nodeHealth{a: a, e: e}
	if e.loginErr == nil {
		h.check = &node{url: e.cfg.Node.RPCURL, user: user, pass: pass, http: &http.Client{Timeout: nodeCheckTimeout}}
	}
	return h
}

func (h *nodeHealth) run(stop <-chan struct{}) {
	t := time.NewTicker(nodeCheckEvery)
	defer t.Stop()
	for {
		h.once()
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

// once is one check.
func (h *nodeHealth) once() {
	if h.check == nil {
		if _, _, err := h.e.cfg.rpcLogin(); err == nil {
			h.a.log.Info("the node's cookie file can be read now: using it")
			h.reapply("the cookie file can be read now")
		}
		return
	}
	ci, err := h.check.chainInfo()
	r := &healthResult{at: time.Now(), ci: ci, err: err, unauthorized: errors.Is(err, errUnauthorized)}
	h.last.Store(r)
	if err == nil {
		h.good.Store(ci)
	}
	if r.unauthorized && h.e.cfg.Node.RPCUser == "" {
		user, pass, err := h.e.cfg.rpcLogin()
		if err == nil && (user != h.check.user || pass != h.check.pass) {
			h.a.log.Info("the node's cookie changed (the node restarted): using the new one")
			h.reapply("the node's cookie changed")
		}
	}
}

// beforeReapply is called by a check about to apply the settings again; a test holds it there.
var beforeReapply = func() {}

// reapply applies the settings again while this check's engine is the one in use: those in the
// file now, never the engine's own, which a save may have replaced a moment ago.
func (h *nodeHealth) reapply(why string) {
	if h.a.eng.Load() != h.e || h.e.stopped() {
		return
	}
	beforeReapply()
	h.a.reloadFromFile(why)
}
