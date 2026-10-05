//go:build !windows

package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// payoutWorld is startFailWorld with every program in place and a dashboard API whose answer about
// the payout address the test sets (status and body), asked every 50 ms. asked counts the questions.
func payoutWorld(t *testing.T, status int, body string, hold chan struct{}) (tp *tips, set func(int, string), asked func() int) {
	t.Helper()
	tp = startFailWorld(t, map[string]string{"bitcoincashIId.exe": sleeper, "elevenseventyfived.exe": sleeper, "api.exe": sleeper, "stratum.exe": sleeper})
	var amu sync.Mutex
	n := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/pool/config" {
			_, _ = w.Write([]byte(`{"result":"stopping"}`))
			return
		}
		if hold != nil {
			<-hold
		}
		amu.Lock()
		s, b := status, body
		n++
		amu.Unlock()
		w.WriteHeader(s)
		_, _ = w.Write([]byte(b))
	}))
	apiPort = portOf(api.URL)
	savedEvery := payoutPollEvery
	payoutPollEvery = 50 * time.Millisecond
	t.Cleanup(func() {
		if hold != nil {
			close(hold)
		}
		endPayoutWatch()
		payoutPollEvery = savedEvery
		api.Close()
	})
	set = func(s int, b string) { amu.Lock(); status, body = s, b; amu.Unlock() }
	asked = func() int { amu.Lock(); defer amu.Unlock(); return n }
	return tp, set, asked
}

const (
	noAddress      = `{"configured":false,"pool_address":""}`
	anAddress      = `{"configured":true,"pool_address":"bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2"}`
	cannotTell     = `{"configured":false,"success":false}`
	severalAnswers = 300 * time.Millisecond // six questions at one every 50 ms
)

// With no payout address set the miner mines nothing (the dashboard's mining status says
// no_payout_address), yet the tray said "running" as soon as the miner started, a second or less
// after it had asked for an address. It now asks for one for as long as the dashboard's API says
// none is set, and says "running" once one is saved: the API is asked every payoutPollEvery, on its
// own. An answer the API cannot give changes nothing, either way.
func TestTheTrayAsksForAPayoutAddressUntilOneIsSet(t *testing.T) {
	tp, set, asked := payoutWorld(t, 200, noAddress, nil)
	boot()
	if !waitFor(5*time.Second, func() bool { return started("stratum") }) {
		t.Fatalf("setup: the miner did not start; log:\n%s", launcherLog())
	}
	minerStart.Wait()
	time.Sleep(severalAnswers)
	if n := asked(); n < 3 {
		t.Fatalf("TIP-ADDRESS-WATCHED: the API was asked %d times in %v, at one question every %v", n, severalAnswers, payoutPollEvery)
	}
	if tp.last() != tipSetAddress {
		t.Fatalf("TIP-ADDRESS-UNTIL-SET: with no payout address set and everything running, the tray says %q (it said %q)", tp.last(), tp.all())
	}

	set(503, cannotTell)
	time.Sleep(severalAnswers)
	if tp.last() != tipSetAddress {
		t.Errorf("TIP-ADDRESS-FAILED-POLL-KEEPS-ASK: an API that could not say whether an address is set made the tray say %q", tp.last())
	}

	set(200, anAddress)
	if !waitFor(2*time.Second, func() bool { return tp.last() == tipRunning }) {
		t.Fatalf("TIP-ADDRESS-SAVED: an address was saved, and the tray still says %q", tp.last())
	}
	if !strings.Contains(launcherLog(), "no payout address is set: the tray asks for one") || !strings.Contains(launcherLog(), "a payout address is set") {
		t.Errorf("TIP-ADDRESS-LOGGED: launcher.log does not say when the tray began and stopped asking:\n%s", launcherLog())
	}

	set(503, cannotTell)
	time.Sleep(severalAnswers)
	if tp.last() != tipRunning {
		t.Errorf("TIP-ADDRESS-FAILED-POLL-KEEPS-RUNNING: an API that could not say whether an address is set made the tray say %q", tp.last())
	}

	set(200, noAddress)
	if !waitFor(2*time.Second, func() bool { return tp.last() == tipSetAddress }) {
		t.Errorf("TIP-ADDRESS-UNSET-AGAIN: the address was taken away, and the tray still says %q", tp.last())
	}
}

// With a payout address set, the tray never asks for one, at the start or later: it asked at
// every start, until the miner ran.
func TestTheTrayDoesNotAskWithAnAddressSet(t *testing.T) {
	tp, _, asked := payoutWorld(t, 200, anAddress, nil)
	boot()
	if !waitFor(5*time.Second, func() bool { return started("stratum") }) {
		t.Fatalf("setup: the miner did not start; log:\n%s", launcherLog())
	}
	minerStart.Wait()
	time.Sleep(severalAnswers)
	if asked() < 3 {
		t.Fatalf("setup: the API was asked %d times", asked())
	}
	if tp.has(tipSetAddress) || tp.last() != tipRunning {
		t.Errorf("TIP-ADDRESS-NOT-ASKED: with an address set, the tray said %q", tp.all())
	}
}

// The question to the API waits for nothing: an API that takes it and does not answer holds neither
// the start nor the miner. The start waited for the answer before it started the miner.
func TestThePayoutAddressQuestionHoldsNothing(t *testing.T) {
	payoutWorld(t, 200, noAddress, make(chan struct{}))
	start := time.Now()
	done := make(chan struct{})
	go func() { boot(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("TIP-ADDRESS-HOLDS-NOTHING: the start did not end in 5 s with an API that does not answer the question; log:\n%s", launcherLog())
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Errorf("TIP-ADDRESS-HOLDS-NOTHING: the start took %v with an API that does not answer the question", took)
	}
	if !waitFor(1500*time.Millisecond-time.Since(start), func() bool { return started("stratum") }) {
		t.Errorf("TIP-ADDRESS-HOLDS-NOTHING: the miner was not started within 1.5 s with an API that does not answer the question; log:\n%s", launcherLog())
	}
	minerStart.Wait()
}

// What is wrong with a program comes first: the tray asks for a payout address only while
// everything starts as it should.
func TestTheTrayAsksForAnAddressAfterTrouble(t *testing.T) {
	saved, savedTip := dataDir, setTooltip
	dataDir = t.TempDir()
	tp := &tips{}
	setTooltip = tp.add
	dashboardOpen.Store(true)
	noPayoutAddress.Store(true)
	t.Cleanup(func() {
		setTooltip, dataDir = savedTip, saved
		dashboardOpen.Store(false)
		resetStartState()
	})
	showRunning()
	if tp.last() != tipSetAddress {
		t.Fatalf("TIP-ADDRESS-SHOWN: with no payout address set the tray says %q", tp.last())
	}
	couldNotStart("bch2", "the BCH2 node", errors.New("held"), time.Minute)
	tp.add("something else")
	showRunning()
	if tp.last() != "Forge Solo cannot start the BCH2 node: held" {
		t.Errorf("TIP-ADDRESS-TROUBLE-FIRST: with the BCH2 node unable to start and no payout address set, the tray says %q", tp.last())
	}
}
