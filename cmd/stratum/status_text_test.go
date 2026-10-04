package main

import (
	"strings"
	"testing"
	"time"
)

// Every message the dashboard's "Not mining" banner shows is plain punctuation, as the rest of the
// dashboard is: three of them had an em-dash.
func TestMiningStatusMessagesArePlainText(t *testing.T) {
	now := time.Now()
	old := now.Add(-2 * jobStaleAfter)
	for _, st := range []miningStatusSnapshot{
		miningStatusFrom(false, true, 0, 0, 0, time.Time{}, time.Time{}, "", now),
		miningStatusFrom(false, false, 0, 0, 0, time.Time{}, time.Time{}, "", now),
		miningStatusFrom(true, true, 0, 0, 0, time.Time{}, time.Time{}, "", now),
		miningStatusFrom(true, true, 0, 0, 0, time.Time{}, time.Time{}, "Loading block index…", now),
		miningStatusFrom(true, true, 1, 1, 100, old, now, "rpc error", now),
		miningStatusFrom(true, true, 0, 0, 100, now, time.Time{}, "", now),
		miningStatusFrom(true, true, 3, 0, 100, now, time.Time{}, "", now),
		miningStatusFrom(true, true, 1, 1, 100, now, time.Time{}, "", now),
	} {
		if st.Message == "" && !st.Mining {
			t.Errorf("TEXT-SETUP: reason %q has no message", st.Reason)
		}
		if strings.Contains(st.Message, "—") {
			t.Errorf("TEXT-NO-EMDASH: %s: %q", st.Reason, st.Message)
		}
	}
}
