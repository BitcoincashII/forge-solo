package main

import (
	"sync"
	"sync/atomic"
)

// What the tray's Try Again does, when it is offered: start Forge Gateway again after a start that
// failed.
const (
	noRetry int32 = iota
	retryStart
)

// retryOffered is what Try Again does now; noRetry while it is not offered.
var retryOffered atomic.Int32

// showTryAgain shows or hides the tray's Try Again (a stand-in until the tray is up, and in the
// tests).
var showTryAgain = func(bool) {}

// offerTryAgain shows Try Again, to do what.
func offerTryAgain(what int32) {
	retryOffered.Store(what)
	showTryAgain(true)
}

// startFailed shows in the tray why Forge Gateway could not start, and offers Try Again. Forge
// Gateway keeps running, with nothing started, so that its tray icon can say so: a second start then
// only opens the status page of this one, which nothing serves.
func startFailed(tip string) {
	status(tip)
	offerTryAgain(retryStart)
}

// What launcher.log says to do after a failed start; the tray says why, and offers Try Again.
const (
	tryAgainAdvice = "then right-click Forge Gateway's tray icon: Try Again"
	closeItAdvice  = "close that program, " + tryAgainAdvice
)

// tryAgain is the tray's Try Again. It acts once per offer: a second click does nothing.
func tryAgain() {
	what := retryOffered.Swap(noRetry)
	if what == noRetry || isStopping() {
		return
	}
	showTryAgain(false)
	logf("trying again")
	if prepErr != nil {
		if prepErr = prepare(); prepErr != nil {
			logf("Forge Gateway cannot start: %v; %s", prepErr, tryAgainAdvice)
			startFailed(tipCannotStart(startWhy(prepErr)))
			return
		}
	}
	startBoot()
}

// bootsUnderWay are the boots started off the tray's menu loop (the tests wait for them).
var bootsUnderWay sync.WaitGroup

// startBoot runs boot off the tray's menu loop: it waits for the gateway's ports and status page.
func startBoot() {
	bootsUnderWay.Add(1)
	go func() {
		defer bootsUnderWay.Done()
		boot()
	}()
}
