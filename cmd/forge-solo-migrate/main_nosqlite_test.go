//go:build !sqlite

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const mainEnv = "FORGE_MIGRATE_TEST_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(mainEnv) == "1" {
		main()
	}
	os.Exit(m.Run())
}

// A migrator built without -tags sqlite cannot write forgesolo.db: it says so and exits 2, so a
// mistaken build fails loudly at the first start instead of moving nothing.
func TestUntaggedBuildRefuses(t *testing.T) {
	cmd := exec.Command(os.Args[0], "run", "--db", "x", "--pgdata", "y", "--owner", "1:1")
	cmd.Env = append(os.Environ(), mainEnv+"=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 || !strings.Contains(stderr.String(), "-tags sqlite") {
		t.Fatalf("MIG-CLI-UNTAGGED: the untagged build ended with %v (%s), want exit 2 naming -tags sqlite", err, stderr.String())
	}
}
