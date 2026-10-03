package forgesolo

import (
	"os"
	"strings"
	"testing"
)

// Settings must not save before the stored settings have been read: until then its boxes hold the
// page's defaults (empty, Solo), and saving them cleared the stored 1175 address and tag and
// switched the payout mode. Rendered in a browser this is checked by hand; here the handler is
// held to checking configLoaded before it posts, and configLoaded to being set only once a read
// succeeded.
func TestSettingsSaveWaitsForTheStoredSettings(t *testing.T) {
	b, err := os.ReadFile("web/dist/settings.html")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	click := strings.Index(s, "getElementById('saveBtn').addEventListener('click'")
	post := strings.Index(s, "fetch('/api/v1/pool/config',{method:'POST'")
	guard := strings.Index(s[click+1:], "if(!configLoaded){")
	if click < 0 || post < 0 || guard < 0 || click+1+guard > post {
		t.Fatalf("SAVE-GUARD: the Save handler does not check configLoaded before it posts")
	}
	applied := strings.Index(s, "applyConfig(d);\n         configLoaded=true;")
	if applied < 0 || strings.Count(s, "configLoaded=true") != 1 {
		t.Fatalf("SAVE-GUARD-SET: configLoaded must be set once, right after a successful read is applied")
	}
}
