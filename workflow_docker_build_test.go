package forgesolo

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The image build's jobs each get the token scopes they use and no more. The build job runs
// third-party actions and a privileged emulator image: it pushes the images (packages: write) and
// nothing else. Only the re-pin pushes to main (contents: write) and starts the Tests run on it
// (actions: write). A job-level permissions block replaces the workflow's, as on GitHub.
func TestDockerBuildLeastPrivilege(t *testing.T) {
	b, err := os.ReadFile(".github/workflows/docker-build.yml")
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
			Steps       []struct {
				Uses string         `yaml:"uses"`
				With map[string]any `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(b, &w); err != nil {
		t.Fatalf("DOCKER-BUILD-PRIVILEGE: permissions are not scope: level maps: %v", err)
	}
	if len(w.Jobs) == 0 {
		t.Fatal("DOCKER-BUILD-PRIVILEGE: docker-build.yml has no jobs")
	}
	writes := func(p map[string]string) []string {
		var s []string
		for scope, level := range p {
			if level == "write" {
				s = append(s, scope)
			}
		}
		slices.Sort(s)
		return s
	}
	if got := writes(w.Permissions); len(got) > 0 {
		t.Errorf("DOCKER-BUILD-PRIVILEGE: the workflow gives every job without its own block write access to %v", got)
	}
	need := map[string][]string{"build": {"packages"}, "repin": {"actions", "contents"}}
	for job, j := range w.Jobs {
		p := w.Permissions
		if j.Permissions != nil {
			p = j.Permissions
		}
		if got := writes(p); !slices.Equal(got, need[job]) {
			t.Errorf("DOCKER-BUILD-PRIVILEGE: job %s can write %v; it needs %v", job, got, need[job])
		}
	}
	// The re-pin's push uses the token its checkout keeps.
	for _, s := range w.Jobs["repin"].Steps {
		if strings.HasPrefix(s.Uses, "actions/checkout@") && s.With["persist-credentials"] == false {
			t.Error("DOCKER-BUILD-REPIN-PUSH: the re-pin's checkout keeps no token, so its push to main would fail")
		}
	}
}

// The image build's step names say which node and PostgreSQL versions each image carries, and the
// release's Actions log is where someone looks that up, so they must match the Dockerfiles.
func TestDockerBuildStepNamesMatchTheImages(t *testing.T) {
	w := loadWorkflow(t, ".github/workflows/docker-build.yml")
	names := map[string]string{}
	for _, s := range w.Jobs["build"].Steps {
		names[s.ID] = s.Name
	}
	read := func(path, pattern string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(pattern).FindSubmatch(b)
		if m == nil {
			t.Fatalf("BUILD-STEP-NAME: no %s in %s", pattern, path)
		}
		return string(m[1])
	}
	want := map[string][]string{
		"node":     {"BCH2 v" + read("docker/node/Dockerfile", `(?m)^ARG BCH2_VERSION=(\S+)$`)},
		"node1175": {"ESF v" + read("docker/node1175/Dockerfile", `(?m)^ARG ESF_VERSION=(\S+)$`)},
		"postgres": {
			"PostgreSQL " + read("docker/postgres/Dockerfile", `(?m)^FROM postgres:([0-9.]+)-`),
			"TimescaleDB " + read("docker/postgres/Dockerfile", `(?m)^FROM timescale/timescaledb:([0-9.]+)-pg[0-9]+@\S+ AS timescale$`),
		},
	}
	for id, parts := range want {
		for _, p := range parts {
			if !regexp.MustCompile(regexp.QuoteMeta(p) + `\b`).MatchString(names[id]) {
				t.Errorf("BUILD-STEP-NAME: the %s build step is named %q, but its image has %s", id, names[id], p)
			}
		}
	}
}
