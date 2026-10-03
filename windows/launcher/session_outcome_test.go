package main

import (
	"testing"
	"time"
)

// sessionWorld resets what the session tests touch.
func sessionWorld(t *testing.T, ending bool) {
	t.Helper()
	savedData, savedWait := dataDir, sessionOutcomeWait
	dataDir = t.TempDir()
	sessionEnding.Store(ending)
	t.Cleanup(func() {
		sessionEnding.Store(false)
		select {
		case <-sessionOutcome:
		default:
		}
		dataDir, sessionOutcomeWait = savedData, savedWait
	})
}

// Windows asked to end the session, Forge Solo stopped, and then the shutdown was cancelled (another
// program held it up, and someone chose Cancel): Forge Solo starts again rather than leave the PC
// not mining. When the session does end, it does not.
func TestACancelledShutdownStartsForgeSoloAgain(t *testing.T) {
	sessionWorld(t, true)
	noteSessionOutcome(false)
	if !relaunchAfterSessionStop() {
		t.Fatal("SESSION-CANCEL-RESTARTS: a cancelled shutdown left Forge Solo stopped")
	}
	noteSessionOutcome(true)
	if relaunchAfterSessionStop() {
		t.Fatal("SESSION-END-EXITS: Forge Solo would start again while Windows ends the session")
	}
}

// Windows never answering is taken as no shutdown: had the session ended, Forge Solo would be gone.
func TestNoAnswerFromWindowsStartsForgeSoloAgain(t *testing.T) {
	sessionWorld(t, true)
	sessionOutcomeWait = 100 * time.Millisecond
	if !relaunchAfterSessionStop() {
		t.Fatal("SESSION-OUTCOME-TIMEOUT: no answer from Windows left Forge Solo stopped")
	}
}

// Quit is not a session end: nothing is waited for, and nothing starts again.
func TestQuitDoesNotStartForgeSoloAgain(t *testing.T) {
	sessionWorld(t, false)
	sessionOutcomeWait = 200 * time.Millisecond
	start := time.Now()
	if relaunchAfterSessionStop() || time.Since(start) > 100*time.Millisecond {
		t.Fatal("SESSION-QUIT-EXITS: Quit started Forge Solo again, or waited on Windows")
	}
}

// An answer to a question Forge Solo was never asked is not kept for a later one: a stale "not
// ending" would start Forge Solo again in the middle of a real shutdown.
func TestAnAnswerWithoutAQuestionIsNotKept(t *testing.T) {
	sessionWorld(t, false)
	noteSessionOutcome(false) // another program refused before Forge Solo was asked
	sessionEnding.Store(true) // later, Windows asks Forge Solo
	sessionOutcomeWait = 100 * time.Millisecond
	noteSessionOutcome(true) // and ends the session
	if relaunchAfterSessionStop() {
		t.Fatal("SESSION-OUTCOME-ONLY-AFTER-QUERY: a stale answer started Forge Solo again in a real shutdown")
	}
}
