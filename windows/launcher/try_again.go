package main

import "sync/atomic"

// What the tray's Try Again does, when it is offered: start Forge Solo again after a start that
// failed, or open the dashboard after another program kept its port.
const (
	noRetry int32 = iota
	retryStart
	retryDashboard
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

// startFailed shows in the tray why Forge Solo could not start, and offers Try Again. Forge Solo
// keeps running, with nothing started, so that its tray icon can say so: a second start then only
// opens the dashboard of this one, which nothing serves.
func startFailed(tip string) {
	status(tip)
	offerTryAgain(retryStart)
}

// The tray's advice after a failed start.
const (
	tryAgainTip = " Right-click here: Try Again."
	closeItTip  = " Close it, then right-click here: Try Again."
	reservedTip = " See launcher.log, then Try Again."
)

// failTip is the tray's tooltip for a start that failed: why, cut short if need be so that the
// advice after it still shows (Windows shows 127 characters).
func failTip(why, advice string) string {
	const prefix = "Forge Solo cannot start: "
	room := 127 - len([]rune(prefix)) - 1 - len([]rune(advice))
	if r := []rune(why); len(r) > room {
		why = string(r[:room-1]) + "…"
	}
	return prefix + why + "." + advice
}

// tryAgain is the tray's Try Again. It acts once per offer: a second click does nothing.
func tryAgain() {
	what := retryOffered.Swap(noRetry)
	if what == noRetry || isStopping() {
		return
	}
	showTryAgain(false)
	switch what {
	case retryStart:
		logf("trying again")
		if prepErr != nil {
			if prepErr = prepare(); prepErr != nil {
				logf("Forge Solo cannot start: %v", prepErr)
				startFailed(failTip(prepErr.Error(), tryAgainTip))
				return
			}
		}
		go boot()
	case retryDashboard:
		logf("opening the dashboard again")
		openDashboard()
	}
}
