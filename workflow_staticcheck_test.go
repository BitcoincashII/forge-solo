package forgesolo

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// staticcheck reads the Windows launcher too, as Windows code, at the version the unit job pins. The
// launcher is a module of its own, so the unit job's ./... never reaches it, and vet does not find
// code nothing reaches.
func TestLauncherIsStaticchecked(t *testing.T) {
	b, err := os.ReadFile(".github/workflows/test.yml")
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		Jobs map[string]struct {
			Steps []struct {
				Run              string `yaml:"run"`
				WorkingDirectory string `yaml:"working-directory"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	pin := regexp.MustCompile(`(?m)^\s*go install honnef\.co/go/tools/cmd/staticcheck@(v[0-9][0-9A-Za-z.-]*)$`)
	var unit string
	for _, s := range w.Jobs["unit"].Steps {
		if m := pin.FindStringSubmatch(s.Run); m != nil {
			unit = m[1]
		}
	}
	if unit == "" {
		t.Fatal("LAUNCHER-STATICCHECK-PIN: the unit job installs no pinned staticcheck")
	}
	run := regexp.MustCompile(`(?m)^\s*GOOS=windows GOARCH=amd64 "\$\(go env GOPATH\)/bin/staticcheck" \./\.\.\.$`)
	for _, s := range w.Jobs["windows"].Steps {
		if s.WorkingDirectory != "windows/launcher" || !run.MatchString(s.Run) {
			continue
		}
		if m := pin.FindStringSubmatch(s.Run); m == nil || m[1] != unit || strings.Index(s.Run, m[0]) > run.FindStringIndex(s.Run)[0] {
			t.Errorf("LAUNCHER-STATICCHECK-PIN: the launcher's staticcheck is not installed at the unit job's %s before it runs", unit)
		}
		return
	}
	t.Error("LAUNCHER-STATICCHECK: no step in the windows job runs staticcheck on windows/launcher with GOOS=windows")
}
