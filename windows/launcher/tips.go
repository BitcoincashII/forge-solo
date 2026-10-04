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
	tipStarting        = "Forge Solo: starting…"
	tipPreparing       = "Forge Solo: preparing…"
	tipStartingNodes   = "Forge Solo: starting the nodes (the first sync takes a while)…"
	tipSetAddress      = "Forge Solo: set your payout address in the dashboard"
	tipRestartingMiner = "Forge Solo: restarting the miner…"
	tipStopping        = "Forge Solo: shutting down cleanly…"
	tipMoving          = "Forge Solo: moving your data (once, a few minutes at most)…"
	tipMoveFailed      = "Forge Solo: the data move failed. Nothing lost: see dashboard"
	tipRunning         = "Forge Solo: running"
)

// What the tray says after "running" (setRunningNote), while Forge Solo runs as it should. With
// the rental port taken it says what to do as well: nothing tries the port again until Forge Solo
// restarts.
const (
	noteDegraded        = ". The old database is damaged: see dashboard"
	noteNoRentals       = ". No rentals: stop what uses " + rentalPort + ", restart"
	noteRentalsReserved = ". No rentals: Windows keeps port " + rentalPort
)

// tipRunningWith is "running" with note after it.
func tipRunningWith(note string) string { return tipRunning + note }

// tipCannotStart is a start that failed, with why in a few words (startWhy).
func tipCannotStart(why string) string { return "Forge Solo cannot start: " + why }

// tipNoDashboard is the dashboard's port held by another program.
func tipNoDashboard(port string) string {
	return "Forge Solo: no dashboard: another program uses port " + port
}

// tipProgramCannotStart is a program that could not start, and why; status cuts a long why short.
func tipProgramCannotStart(what, why string) string {
	return "Forge Solo cannot start " + what + ": " + why
}

// tipRestarting is a program that stopped on its own, started again.
func tipRestarting(what string) string { return "Forge Solo: " + what + " stopped; starting it again" }

// tipRebuilding is a node rebuilding its chain data, which it found damaged (-reindex).
func tipRebuilding(what string) string {
	return "Forge Solo: rebuilding " + what + "'s chain (a few minutes)…"
}

// tipChainDamaged is a node whose chain data is still damaged after it was rebuilt.
func tipChainDamaged(what string) string {
	return "Forge Solo: " + what + "'s chain is damaged: see launcher.log"
}

// startWhy is why a start failed, in the few words the tray has room for: a port's own short form,
// or what the error says before its details in brackets.
func startWhy(err error) string {
	var pe *portError
	if errors.As(err, &pe) {
		return pe.trayWhy()
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
