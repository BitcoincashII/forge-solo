package stats

import "testing"

// The old default reads as no tag chosen; anything else is a choice and is kept as stored.
func TestOldDefaultCoinbaseTagReadsAsNoneChosen(t *testing.T) {
	for stored, want := range map[string]string{
		"Forge":         "",
		"":              "",
		"forge":         "forge",
		"Forge ":        "Forge ",
		"/Forge/":       "/Forge/",
		"MyRig":         "MyRig",
		"//forgesolo//": "//forgesolo//",
	} {
		if got := chosenCoinbaseTag(stored); got != want {
			t.Errorf("stored %q reads as %q, want %q", stored, got, want)
		}
	}
}
