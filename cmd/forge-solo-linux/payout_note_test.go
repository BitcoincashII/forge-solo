package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAPI answers /api/v1/pool/config as the API does: 503 while its database does not answer (a
// 503 is no answer, whatever its body says), then with configured as answer says ("" leaves the
// field out).
func fakeAPI(t *testing.T, failFirst int, answer string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/pool/config" {
			http.NotFound(w, r)
			return
		}
		if n := asked.Add(1); int(n) <= failFirst {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"success":false,"configured":false,"error":"Forge Solo cannot read its saved settings right now"}`)
			return
		}
		if answer == "" {
			io.WriteString(w, `{"pool_name":"Forge Solo"}`)
			return
		}
		io.WriteString(w, `{"configured":`+answer+`,"pool_address":""}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

// forge-solo run says that mining waits for a payout address only when none is saved: the banner
// said so at every start, also with one saved. Whether one is saved is the API's to say, as it
// says it to Settings, once it answers.
func TestPayoutNoteOnlyWithoutAnAddress(t *testing.T) {
	savedEvery, savedFor, savedOut := payoutAskEvery, payoutAskFor, logOut
	t.Cleanup(func() { payoutAskEvery, payoutAskFor, logOut = savedEvery, savedFor, savedOut })
	payoutAskEvery = 10 * time.Millisecond

	var b strings.Builder
	banner(&b, "127.0.0.1:3080", "/d", false, true)
	if strings.Contains(b.String(), "payout address") {
		t.Errorf("BANNER-PAYOUT-ALWAYS: the banner says it at every start:\n%s", b.String())
	}

	ask := func(failFirst int, answer string, within time.Duration) (saved, known bool, asked int32) {
		t.Helper()
		srv, n := fakeAPI(t, failFirst, answer)
		ctx, cancel := context.WithTimeout(context.Background(), within)
		defer cancel()
		saved, known = payoutAddressSaved(ctx, srv.URL)
		return saved, known, n.Load()
	}
	if saved, known, asked := ask(2, "true", 5*time.Second); !saved || !known || asked != 3 {
		t.Errorf("PAYOUT-SAVED: an API that answers on its third ask with an address saved gave saved %v, known %v after %d asks", saved, known, asked)
	}
	if saved, known, _ := ask(0, "false", 5*time.Second); saved || !known {
		t.Errorf("PAYOUT-UNSET: an API that says no address is saved gave saved %v, known %v", saved, known)
	}
	if _, known, asked := ask(1<<30, "true", 200*time.Millisecond); known || asked < 2 {
		t.Errorf("PAYOUT-WAITS: an API whose database does not answer gave known %v after %d asks; want unknown, asked again", known, asked)
	}
	if _, known, _ := ask(0, "", 200*time.Millisecond); known {
		t.Error("PAYOUT-FIELD: an answer without configured was taken as one")
	}

	if note := payoutNote(true, true); note != "" {
		t.Errorf("PAYOUT-NOTE-SAVED: with an address saved it says %q", note)
	}
	if note := payoutNote(false, true); !strings.Contains(note, "set your BCH2 payout address") || !strings.Contains(note, "mining waits for it") {
		t.Errorf("PAYOUT-NOTE-UNSET: with no address saved it says %q", note)
	}
	if note := payoutNote(false, false); !strings.HasPrefix(note, "if no BCH2 payout address is saved yet") {
		t.Errorf("PAYOUT-NOTE-UNKNOWN: with no answer from the API it says %q", note)
	}

	// What forge-solo run logs, once the API answers.
	say := func(failFirst int, answer string, stopped bool) string {
		t.Helper()
		out := &syncBuffer{}
		logOut = out
		srv, _ := fakeAPI(t, failFirst, answer)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if stopped {
			cancel()
		}
		sayPayoutAddress(ctx, srv.URL)
		cancel()
		return out.String()
	}
	if out := say(1, "true", false); out != "" {
		t.Errorf("PAYOUT-SAY-SAVED: with an address saved, forge-solo run logged %q", out)
	}
	if out := say(1, "false", false); !strings.Contains(out, "set your BCH2 payout address in the dashboard's Settings: mining waits for it") {
		t.Errorf("PAYOUT-SAY-UNSET: with no address saved, forge-solo run logged %q", out)
	}
	if out := say(1, "false", true); out != "" {
		t.Errorf("PAYOUT-SAY-STOPPED: stopping, forge-solo run logged %q", out)
	}
	payoutAskFor = 300 * time.Millisecond
	if out := say(1<<30, "true", false); !strings.Contains(out, "if no BCH2 payout address is saved yet") {
		t.Errorf("PAYOUT-SAY-UNKNOWN: with no answer from the API within its time, forge-solo run logged %q", out)
	}

	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `go sayPayoutAddress(ctx, "http://127.0.0.1:"+apiPort)`) {
		t.Error("PAYOUT-WIRED: forge-solo run does not ask its API whether a payout address is saved")
	}
}
