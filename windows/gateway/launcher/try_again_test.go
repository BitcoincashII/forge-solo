package main

import (
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// boots counts the starts of the tray's boot, by the tooltip each begins with.
func (w *world) boots() int {
	n := 0
	for _, s := range w.tips.all() {
		if s == tipPreparing {
			n++
		}
	}
	return n
}

// Another program on the miner port: Forge Gateway cannot start, and keeps its tray icon, which
// says why and offers Try Again. Try Again with the port still taken says so again; with it free,
// the gateway starts. A second click does nothing.
func TestTryAgainAfterAPortWasTaken(t *testing.T) {
	w := gatewayWorld(t, "status")
	n, _ := strconv.Atoi(stratumPort)
	var held atomic.Bool
	held.Store(true)
	tcpListeners = func() []tcpListener {
		if held.Load() {
			return []tcpListener{{net.IPv4zero.To4(), n, 4242}}
		}
		return nil
	}
	boot()
	want := "Forge Gateway cannot start: another program uses port " + stratumPort
	if w.tips.last() != want || strings.Join(w.tryShown.all(), " ") != "true" {
		t.Fatalf("GWL-TRY-AGAIN-OFFERED: the tray says %q, Try Again %v", w.tips.last(), w.tryShown.all())
	}
	tryAgain()
	if !waitFor(5*time.Second, func() bool { return len(w.tryShown.all()) == 3 }) || w.tips.last() != want || w.boots() != 2 || w.starts() != 0 {
		t.Fatalf("GWL-TRY-AGAIN-STILL-TAKEN: with the port still taken, Try Again gave %q, Try Again %v, %d boots", w.tips.last(), w.tryShown.all(), w.boots())
	}
	held.Store(false)
	tryAgain()
	if !waitFor(10*time.Second, func() bool { return len(w.opened.all()) == 1 }) {
		t.Fatalf("GWL-TRY-AGAIN: with the port free, Try Again did not start Forge Gateway; log:\n%s", launcherLog())
	}
	tryAgain()
	time.Sleep(500 * time.Millisecond)
	if w.boots() != 3 || w.starts() != 1 || !started(gatewayKey) || len(w.tryShown.all()) != 4 {
		t.Fatalf("GWL-TRY-AGAIN-ONCE: %d boots and %d starts of the gateway, Try Again %v; want 3, 1 and shown, hidden, shown, hidden", w.boots(), w.starts(), w.tryShown.all())
	}
	if !strings.Contains(launcherLog(), "another program uses port "+stratumPort+", the miner port (") || !strings.Contains(launcherLog(), closeItAdvice) {
		t.Errorf("GWL-TRY-AGAIN-LOGGED: launcher.log does not say which port, and what to do:\n%s", launcherLog())
	}
}

// secrets.env could not be read (another program held it a moment): Try Again reads it again, and
// with it readable, Forge Gateway starts.
func TestTryAgainAfterSecretsCouldNotBeRead(t *testing.T) {
	w := gatewayWorld(t, "status")
	sec = secrets{}
	if err := os.Mkdir(dpath("secrets.env"), 0o755); err != nil {
		t.Fatal(err)
	}
	prepErr = errors.New("secrets.env cannot be read")
	offerTryAgain(retryStart)
	tryAgain()
	if !strings.HasPrefix(w.tips.last(), "Forge Gateway cannot start: secrets.env cannot be read") || retryOffered.Load() != retryStart || w.boots() != 0 {
		t.Fatalf("GWL-TRY-AGAIN-PREPARE-FAILS: the tray says %q, offered %d", w.tips.last(), retryOffered.Load())
	}
	_ = os.Remove(dpath("secrets.env"))
	tryAgain()
	if !waitFor(10*time.Second, func() bool { return len(w.opened.all()) == 1 }) || prepErr != nil || w.starts() != 1 {
		t.Fatalf("GWL-TRY-AGAIN-PREPARE: with secrets.env readable, Try Again did not start Forge Gateway (%v); log:\n%s", prepErr, launcherLog())
	}
	if len(sec.Settings) != 64 {
		t.Errorf("GWL-TRY-AGAIN-PREPARE: Try Again made no Settings password")
	}
}
