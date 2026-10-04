package main

import (
	"os"
	"strings"
	"testing"
)

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

// On Linux, Settings names the secrets.env that holds the password. The service's is in
// /var/lib/forge-solo and a copy run by hand has its own, so "secrets.env in the Forge Solo data
// directory" sent people to the wrong one.
func TestSecretsPathFromEnv(t *testing.T) {
	for _, tc := range []struct{ code, platform, db, want string }{
		{"SECRETS-PATH-SERVICE", "linux", "/var/lib/forge-solo/forgesolo.db", "/var/lib/forge-solo/secrets.env"},
		{"SECRETS-PATH-BY-HAND", "linux", "/home/alice/.local/share/forge-solo/forgesolo.db", "/home/alice/.local/share/forge-solo/secrets.env"},
		{"SECRETS-PATH-NO-DB", "linux", "", ""},
		{"SECRETS-PATH-WINDOWS", "windows", "/x/forgesolo.db", ""},
		{"SECRETS-PATH-UMBREL", "", "/data/forgesolo.db", ""},
	} {
		t.Setenv("FORGE_PLATFORM", tc.platform)
		t.Setenv("DB_PATH", tc.db)
		if got := secretsPathFromEnv(); got != tc.want {
			t.Errorf("%s: FORGE_PLATFORM=%q DB_PATH=%q gives %q, want %q", tc.code, tc.platform, tc.db, got, tc.want)
		}
	}
}

// secretsPathFromEnv finds secrets.env beside the database, which is where Forge Solo for Linux
// keeps both.
func TestLinuxKeepsSecretsBesideTheDatabase(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile("../forge-solo-linux/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	apiEnv := read("main.go")
	if i := strings.Index(apiEnv, "func apiEnv("); i >= 0 {
		apiEnv = apiEnv[i:]
		if j := strings.Index(apiEnv, "\n}\n"); j >= 0 {
			apiEnv = apiEnv[:j]
		}
	} else {
		apiEnv = ""
	}
	if !strings.Contains(apiEnv, `"DB_PATH=" + filepath.Join(dataDir, "forgesolo.db")`) {
		t.Error("SECRETS-BESIDE-DB: the Linux launcher's apiEnv no longer gives the API DB_PATH=<data directory>/forgesolo.db")
	}
	if !strings.Contains(read("setup.go"), `filepath.Join(dataDir, "secrets.env")`) {
		t.Error("SECRETS-BESIDE-DB: the Linux launcher no longer keeps secrets.env in the data directory")
	}
}
