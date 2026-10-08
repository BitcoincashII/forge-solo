package main

import (
	"errors"
	"strings"
)

// What the tray says. Windows 11 shows only the first 64 characters of a tray icon's tooltip
// (Windows 10, 127), so every text the tray shows is made here, in at most tipMax characters with
// the point first; launcher.log has the rest. tips_test.go measures every one, with every value the
// code gives it, and checks that the tray shows nothing else.
const tipMax = 63

const (
	tipStarting        = "Forge Gateway: starting…"
	tipPreparing       = "Forge Gateway: preparing…"
	tipSetUp           = "Forge Gateway: set your node and payout address in Settings"
	tipActive          = "Forge Gateway: mining into Forge Pool's TIDES window"
	tipPoolSolo        = "Forge Gateway: Forge Pool unreachable, mining solo meanwhile"
	tipPoolWaiting     = "Forge Gateway: Forge Pool unreachable, miners turned away"
	tipWaitingForWork  = "Forge Gateway: waiting for work from your node and the pool"
	tipNodeUnreachable = "Forge Gateway: cannot reach your node: see the status page"
	tipNodeLogin       = "Forge Gateway: cannot log in to your node: see Settings"
	tipNodeSyncing     = "Forge Gateway: your node is still syncing"
	tipRunning         = "Forge Gateway: running"
	tipRestartingGW    = "Forge Gateway: restarting…"
	tipStopping        = "Forge Gateway: shutting down cleanly…"
	tipConfigMistake   = "Forge Gateway: its config has a mistake: see launcher.log"
	tipPortClosing     = "Forge Gateway: waiting for its ports to be free again…"
)

// tipCannotStart is a start that failed, with why in a few words (startWhy).
func tipCannotStart(why string) string { return "Forge Gateway cannot start: " + why }

// tipProgramCannotStart is a program that could not start, and why; status cuts a long why short.
func tipProgramCannotStart(what, why string) string {
	return "Forge Gateway cannot start " + what + ": " + why
}

// tipRestarting is a program that stopped on its own, started again.
func tipRestarting(what string) string {
	return "Forge Gateway: " + what + " stopped; starting it again"
}

// tipForState is what the tray says for a state and mode of the gateway (stateTips).
func tipForState(state, mode string) string {
	if state == "pool_unreachable" && mode == "waiting" {
		return tipPoolWaiting
	}
	if tip, ok := stateTips[state]; ok {
		return tip
	}
	return tipRunning
}

// startWhy is why a start failed, in the few words the tray has room for: an error's own short
// form, or what the error says before its details in brackets.
func startWhy(err error) string {
	var short interface{ trayWhy() string }
	if errors.As(err, &short) {
		return short.trayWhy()
	}
	why, _, _ := strings.Cut(err.Error(), " (")
	return why
}

// trimTip cuts a tray text to tipMax characters, the last an ellipsis.
func trimTip(s string) string {
	if r := []rune(s); len(r) > tipMax {
		return string(r[:tipMax-1]) + "…"
	}
	return s
}
