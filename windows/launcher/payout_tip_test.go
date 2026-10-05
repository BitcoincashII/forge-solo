package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// The tray said "set your payout address in the dashboard" at every start, for about a second
// until the miner ran, also with an address set. It asks only when the dashboard's API says none is
// set, and an answer the API cannot give (no answer, an answer that is not one because the database
// cannot be read, or one without the field) changes nothing. An API that takes the question and
// never answers holds the watch a few seconds at most.
func TestTheTrayAsksForAPayoutAddressOnlyWhenNoneIsSet(t *testing.T) {
	savedAPI, savedWait := apiPort, payoutAskTimeout
	t.Cleanup(func() { apiPort, payoutAskTimeout = savedAPI, savedWait })

	// What the API's answer means.
	var mu sync.Mutex
	var status int
	var body string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/pool/config" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		s, b := status, body
		mu.Unlock()
		w.WriteHeader(s)
		_, _ = w.Write([]byte(b))
	}))
	_, apiPort, _ = net.SplitHostPort(api.Listener.Addr().String())
	for _, c := range []struct {
		code, body   string
		status       int
		unset, known bool
	}{
		{"TIP-ADDRESS-SET", `{"configured":true,"pool_address":"bitcoincashii:qzs6rgdp5xs6rgdp5xs6rgdp5xs6rgdp5yc72xjxq2"}`, 200, false, true},
		{"TIP-ADDRESS-UNSET", `{"configured":false,"pool_address":""}`, 200, true, true},
		{"TIP-ADDRESS-NOT-OK", `{"configured":false,"success":false}`, 503, false, false},
		{"TIP-ADDRESS-NO-FIELD", `{"pool_address":""}`, 200, false, false},
	} {
		mu.Lock()
		status, body = c.status, c.body
		mu.Unlock()
		if unset, known := askPayoutAddress(); unset != c.unset || known != c.known {
			t.Errorf("%s: the API answered %d %s, taken as no payout address set %v, known %v", c.code, c.status, c.body, unset, known)
		}
	}
	api.Close()
	if unset, known := askPayoutAddress(); unset || known {
		t.Errorf("TIP-ADDRESS-NO-API: with no API answering, the answer is taken as unset %v, known %v", unset, known)
	}

	// An API that takes the question and never answers holds the watch only as long as
	// payoutAskTimeout, and its silence is no answer.
	if payoutAskTimeout <= 0 || payoutAskTimeout > 5*time.Second {
		t.Errorf("TIP-ADDRESS-WAIT: one question to the API may take %v", payoutAskTimeout)
	}
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	_, apiPort, _ = net.SplitHostPort(slow.Listener.Addr().String())
	payoutAskTimeout = 200 * time.Millisecond
	answer := make(chan bool, 1)
	go func() { _, known := askPayoutAddress(); answer <- known }()
	waited := false
	select {
	case known := <-answer:
		if known {
			t.Error("TIP-ADDRESS-SLOW-API: an API that does not answer is taken as an answer")
		}
	case <-time.After(5 * time.Second):
		waited = true
		t.Error("TIP-ADDRESS-SLOW-API: with an API that takes the question and never answers, the watch still waits after 5 s")
	}
	close(release)
	if waited {
		<-answer
	}
	slow.Close()

	// Often enough that the tray says "running" soon after an address is saved, and no more.
	if payoutPollEvery < 5*time.Second || payoutPollEvery > 30*time.Second {
		t.Errorf("TIP-ADDRESS-INTERVAL: the API is asked every %v, not every 5 to 30 s", payoutPollEvery)
	}
}

// endPayoutWatch ends the watch of the payout address a test started (watchPayoutAddress), waits for
// it, and forgets what it was told.
func endPayoutWatch() {
	mu.Lock()
	was := stopping
	stopping = true
	mu.Unlock()
	payoutWatch.Wait()
	mu.Lock()
	stopping = was
	mu.Unlock()
	noPayoutAddress.Store(false)
}
