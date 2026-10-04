package forgesolo

import (
	"fmt"
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
		"migrate":  {"PostgreSQL " + read("docker/migrate/Dockerfile", `(?m)^FROM postgres:([0-9.]+)-`)},
	}
	for id, parts := range want {
		for _, p := range parts {
			if !regexp.MustCompile(regexp.QuoteMeta(p) + `\b`).MatchString(names[id]) {
				t.Errorf("BUILD-STEP-NAME: the %s build step is named %q, but its image has %s", id, names[id], p)
			}
		}
	}
}

// The migrate image is built with every release from the release's commit, as the api and the
// stratum are, for both platforms, and tagged with the release's version, which its build compiles
// in. The re-pin writes its digest into the compose. The database image of earlier releases is no
// longer built.
func TestDockerBuildMakesTheMigrateImage(t *testing.T) {
	b, err := os.ReadFile(".github/workflows/docker-build.yml")
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		Jobs map[string]struct {
			Outputs map[string]string `yaml:"outputs"`
			Steps   []struct {
				ID   string            `yaml:"id"`
				Uses string            `yaml:"uses"`
				Env  map[string]string `yaml:"env"`
				Run  string            `yaml:"run"`
				With map[string]any    `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	const version = "${{ steps.version.outputs.VERSION }}"
	built := false
	for _, s := range w.Jobs["build"].Steps {
		with := func(k string) string { return strings.TrimSpace(fmt.Sprint(s.With[k])) }
		if strings.Contains(with("file"), "docker/postgres") || strings.Contains(with("tags"), "-postgres:") {
			t.Errorf("BUILD-NO-POSTGRES: step %q still builds the database image of earlier releases", s.ID)
		}
		if s.ID != "migrate" {
			continue
		}
		built = true
		if !strings.HasPrefix(s.Uses, "docker/build-push-action@") || with("context") != "." || with("file") != "./docker/migrate/Dockerfile" ||
			with("platforms") != "linux/amd64,linux/arm64" || with("push") != "true" {
			t.Errorf("BUILD-MIGRATE: the migrate step does not build ./docker/migrate/Dockerfile from the repo for both platforms and push it: %s %v", s.Uses, s.With)
		}
		if with("tags") != "${{ env.REGISTRY }}/${{ env.IMAGE_PREFIX }}-migrate:"+version {
			t.Errorf("BUILD-MIGRATE-TAG: the migrate image is tagged %q, not with the release's version", with("tags"))
		}
		if !slices.Contains(strings.Split(with("build-args"), "\n"), "VERSION="+version) {
			t.Errorf("BUILD-MIGRATE-VERSION: the migrate build is not given the release's version (build-args %q)", with("build-args"))
		}
	}
	if !built {
		t.Fatal("BUILD-MIGRATE: docker-build.yml builds no migrate image")
	}
	out := w.Jobs["build"].Outputs
	if out["migrate"] != "${{ steps.migrate.outputs.digest }}" {
		t.Errorf("BUILD-MIGRATE-DIGEST: the build job's migrate output is %q, not the migrate step's digest", out["migrate"])
	}
	if _, ok := out["postgres"]; ok {
		t.Error("BUILD-NO-POSTGRES: the build job still outputs a postgres digest")
	}
	repinned := false
	for _, s := range w.Jobs["repin"].Steps {
		if strings.Contains(s.Run, "scripts/repin-release.sh") {
			repinned = true
			if s.Env["D_MIGRATE"] != "${{ needs.build.outputs.migrate }}" ||
				!regexp.MustCompile(`scripts/repin-release\.sh "\$V"( "\$D_[A-Z0-9]+"){5} "\$D_MIGRATE"\s*$`).MatchString(s.Run) {
				t.Errorf("REPIN-MIGRATE: the re-pin does not pass the migrate digest as repin-release.sh's last argument:\n%s", s.Run)
			}
		}
	}
	if !repinned {
		t.Error("REPIN-MIGRATE: the repin job does not run scripts/repin-release.sh")
	}
}

// The migrate image's own build: the migrator compiled with -tags sqlite and the version it is
// given, PostgreSQL 16 (the major version of the data it reads), every base image pinned by
// digest, and two checks that fail the build: the server starts far enough to print its version
// (a library left out stops it), and the migrator answers (a build without -tags sqlite exits 2).
func TestMigrateImageBuild(t *testing.T) {
	b, err := os.ReadFile("docker/migrate/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	froms := regexp.MustCompile(`(?m)^FROM .*$`).FindAllString(src, -1)
	if len(froms) < 3 {
		t.Fatalf("MIGRATE-IMAGE-STAGES: %d stages, want the build, PostgreSQL and the image itself", len(froms))
	}
	for _, f := range froms {
		if !regexp.MustCompile(`@sha256:[0-9a-f]{64}( AS \w+)?$`).MatchString(f) {
			t.Errorf("MIGRATE-IMAGE-PINNED: %q is not pinned by digest", f)
		}
	}
	pg := regexp.MustCompile(`(?m)^FROM postgres:(16\.[0-9]+)-`).FindStringSubmatch(src)
	if pg == nil {
		t.Fatal("MIGRATE-IMAGE-PG16: the image does not take PostgreSQL 16, the version of the data it reads")
	}
	for code, re := range map[string]string{
		"MIGRATE-IMAGE-SQLITE":     `go build -tags sqlite [^\n]*(\\\n[^\n]*)*\./cmd/forge-solo-migrate`,
		"MIGRATE-IMAGE-VERSION":    `-X main\.version=\$VERSION`,
		"MIGRATE-IMAGE-CHECK-PG":   `postgres --version \| grep -F ' ` + regexp.QuoteMeta(pg[1]) + `'`,
		"MIGRATE-IMAGE-CHECK-TAG":  `forge-solo-migrate plan --help`,
		"MIGRATE-IMAGE-PG-USER":    `adduser [^\n]*-u 70 `,
		"MIGRATE-IMAGE-ENTRYPOINT": `(?m)^ENTRYPOINT \["/usr/local/bin/forge-solo-migrate"\]$`,
	} {
		if !regexp.MustCompile(re).MatchString(src) {
			t.Errorf("%s: docker/migrate/Dockerfile has no %s", code, re)
		}
	}
}
