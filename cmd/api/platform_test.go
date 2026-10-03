package main

import "testing"

// The settings read says where the app runs, so the page can say where its password is: the
// Windows and Linux launchers set FORGE_PLATFORM; anything else, or nothing, is Umbrel.
func TestPlatformFromEnv(t *testing.T) {
	for env, want := range map[string]string{"": "umbrel", "windows": "windows", " Linux ": "linux", "umbrel": "umbrel", "plan9": "umbrel"} {
		t.Setenv("FORGE_PLATFORM", env)
		if got := platformFromEnv(); got != want {
			t.Errorf("PLATFORM: FORGE_PLATFORM=%q gives %q, want %q", env, got, want)
		}
	}
}
