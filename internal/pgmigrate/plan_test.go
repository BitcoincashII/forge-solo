package pgmigrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type planCase struct {
	Name       string            `json:"name"`
	Files      map[string]string `json:"files"`
	Unreadable []string          `json:"unreadable"`
	Want       string            `json:"want"`
}

func planCases(t *testing.T) []planCase {
	t.Helper()
	b, err := os.ReadFile("testdata/plan-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases []planCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Cases
}

// caseContent is a file's content as plan-cases.json describes it.
func caseContent(t *testing.T, spec string) []byte {
	t.Helper()
	switch {
	case strings.HasPrefix(spec, "@marker:"):
		sum := sha256.Sum256(caseContent(t, strings.TrimPrefix(spec, "@marker:")))
		b, _ := json.Marshal(Marker{PGControlSHA256: hex.EncodeToString(sum[:]), PGVersion: "16", Mode: ModeMove})
		return b
	case strings.HasPrefix(spec, "@flip:"):
		b := caseContent(t, "@"+strings.TrimPrefix(spec, "@flip:"))
		b[100] ^= 0xff
		return b
	case strings.HasPrefix(spec, "@"):
		b, err := os.ReadFile(filepath.Join("testdata", strings.TrimPrefix(spec, "@")))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	return []byte(spec)
}

// layOut makes a case's files under a new folder, its unreadable ones unreadable, and returns the
// database and the old-data paths.
func layOut(t *testing.T, c planCase) (db, pgdata string) {
	t.Helper()
	root := t.TempDir()
	for name, spec := range c.Files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, caseContent(t, spec), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range c.Unreadable {
		unreadable(t, filepath.Join(root, filepath.FromSlash(name)))
	}
	return filepath.Join(root, "data", "forgesolo.db"), filepath.Join(root, "pgdata")
}

// Every case of the decision table both platforms follow.
func TestPlanCases(t *testing.T) {
	cases := planCases(t)
	if len(cases) < 20 {
		t.Fatalf("MIG-PLAN-CASES: plan-cases.json has %d cases", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			d := Plan(layOut(t, c))
			if d.Action != c.Want {
				t.Fatalf("MIG-PLAN: %s: Plan says %s, want %s", c.Name, d, c.Want)
			}
			if hasReason := d.Reason != ""; hasReason != (d.Action == ActionDegraded || d.Action == ActionFailed) {
				t.Fatalf("MIG-PLAN-REASON: %s: %s", c.Name, d)
			}
		})
	}
}

// What forge-solo-migrate plan prints.
func TestDecisionString(t *testing.T) {
	for _, c := range []struct {
		d    Decision
		want string
	}{
		{Decision{Action: ActionNone}, "none"},
		{Decision{Action: ActionDegraded, Reason: "the old database's pg_control is missing"}, "degraded:the old database's pg_control is missing"},
	} {
		if got := c.d.String(); got != c.want {
			t.Errorf("MIG-PLAN-STRING: %q, want %q", got, c.want)
		}
	}
}

// The control file: a clean shutdown, a running cluster, and what is not PostgreSQL 16's.
func TestReadControl(t *testing.T) {
	dir := t.TempDir()
	write := func(b []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, "global"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "global", "pg_control"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ReadControl(dir); err != ErrControlMissing {
		t.Fatalf("MIG-CONTROL-MISSING: %v", err)
	}
	write(caseContent(t, "@pg_control-shutdown"))
	c, err := ReadControl(dir)
	if err != nil || c.State != StateShutDown || c.Version != ControlVersion16 || c.SystemIdentifier == 0 {
		t.Fatalf("MIG-CONTROL-SHUTDOWN: %+v %v", c, err)
	}
	sum := sha256.Sum256(caseContent(t, "@pg_control-shutdown"))
	if c.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("MIG-CONTROL-HASH: %s", c.SHA256)
	}
	write(caseContent(t, "@pg_control-inproduction"))
	if c, err := ReadControl(dir); err != nil || c.State != StateInProduction {
		t.Fatalf("MIG-CONTROL-RUNNING: %+v %v", c, err)
	}
	for _, bad := range []string{"@flip:pg_control-shutdown", "garbage"} {
		write(caseContent(t, bad))
		if _, err := ReadControl(dir); err == nil || !strings.Contains(err.Error(), ErrControlDamaged.Error()) {
			t.Fatalf("MIG-CONTROL-DAMAGED: %s read as %v", bad, err)
		}
	}
	b := caseContent(t, "@pg_control-shutdown")
	b[8] = 0x15 // pg_control_version 1301: not PostgreSQL 16
	write(b)
	if _, err := ReadControl(dir); err == nil {
		t.Fatalf("MIG-CONTROL-VERSION: another version's control file was read as PostgreSQL 16's")
	}
}
