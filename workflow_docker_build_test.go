package forgesolo

import (
	"os"
	"regexp"
	"testing"
)

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
