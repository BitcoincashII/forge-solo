package forgesolo

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A shippedBuild is one go build of the api, the stratum or the migrator in what makes a release.
type shippedBuild struct {
	where, cmd string // the file and line; the command built (api, stratum, forge-solo-migrate)
	sqlite     bool   // built with -tags sqlite
}

var (
	goBuildCmd  = regexp.MustCompile(`"?\./cmd/([\w$-]+)"?`)
	sqliteTag   = regexp.MustCompile(`-tags[ =]"?[\w ]*\bsqlite\b`)
	forProgLoop = regexp.MustCompile(`^\s*for prog in ([\w -]+); do`)
	shippedCmds = []string{"api", "stratum", "forge-solo-migrate"}
)

// shippedBuilds reads every go build of the three commands in file. A line ending in a backslash
// goes on on the next one, and ./cmd/$prog is each command of the for prog loop around it.
func shippedBuilds(t *testing.T, file string) []shippedBuild {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var out []shippedBuild
	var loop []string
	lines := strings.Split(string(b), "\n")
	for i := 0; i < len(lines); i++ {
		n, line := i+1, lines[i]
		for strings.HasSuffix(strings.TrimRight(line, " "), `\`) && i+1 < len(lines) {
			i++
			line = strings.TrimSuffix(strings.TrimRight(line, " "), `\`) + " " + lines[i]
		}
		if m := forProgLoop.FindStringSubmatch(line); m != nil {
			loop = strings.Fields(m[1])
		}
		if strings.TrimSpace(line) == "done" {
			loop = nil
		}
		if !strings.Contains(line, "go build") {
			continue
		}
		for _, m := range goBuildCmd.FindAllStringSubmatch(line, -1) {
			cmds := []string{m[1]}
			if m[1] == "$prog" {
				cmds = loop
			}
			for _, c := range cmds {
				if slices.Contains(shippedCmds, c) {
					out = append(out, shippedBuild{where: file + ":" + strconv.Itoa(n), cmd: c, sqlite: sqliteTag.MatchString(line)})
				}
			}
		}
	}
	return out
}

// composeDBEnv is how docker-compose.yml points each service at its database: "DB_PATH" for the
// SQLite file, "DB_HOST" for a PostgreSQL server, "" for neither.
func composeDBEnv(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Services map[string]struct {
			Environment any `yaml:"environment"` // a list of KEY=value, or a map
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for name, s := range c.Services {
		var keys []string
		switch env := s.Environment.(type) {
		case []any:
			for _, e := range env {
				k, _, _ := strings.Cut(fmt.Sprint(e), "=")
				keys = append(keys, k)
			}
		case map[string]any:
			for k := range env {
				keys = append(keys, k)
			}
		}
		for _, k := range keys {
			if k == "DB_PATH" || k == "DB_HOST" {
				out[name] += k
			}
		}
	}
	return out
}

// Every platform runs the api and the stratum on SQLite, and the migrator writes forgesolo.db. A
// build without -tags sqlite still builds, and then looks for a PostgreSQL server that is not
// there (the migrator says it was built without it, and moves nothing), so each shipped build is
// read here: the Windows release, the Linux tarballs, and the Umbrel images. An image is built for
// the database its service is given in docker-compose.yml: on SQLite (DB_PATH) with the tag, on
// PostgreSQL (DB_HOST) without it; the migrator's image always with it.
func TestShippedBuildsUseSQLite(t *testing.T) {
	for _, c := range []struct {
		code, file string
		want       []string
	}{
		{"BUILD-TAG-WINDOWS", ".github/workflows/release.yml", []string{"api", "stratum", "forge-solo-migrate"}},
		{"BUILD-TAG-LINUX", "scripts/linux/build-release.sh", []string{"api", "stratum"}},
	} {
		builds := shippedBuilds(t, c.file)
		var built []string
		for _, b := range builds {
			built = append(built, b.cmd)
			if !b.sqlite {
				t.Errorf("%s: %s builds %s without -tags sqlite", c.code, b.where, b.cmd)
			}
		}
		for _, w := range c.want {
			if !slices.Contains(built, w) {
				t.Errorf("%s: %s no longer builds %s (it builds %v)", c.code, c.file, w, built)
			}
		}
	}

	db := composeDBEnv(t)
	files, err := filepath.Glob("docker/*/Dockerfile")
	if err != nil || len(files) == 0 {
		t.Fatalf("no Dockerfiles: %v", err)
	}
	images := map[string]bool{}
	for _, f := range files {
		for _, b := range shippedBuilds(t, f) {
			images[b.cmd] = true
			service := filepath.Base(filepath.Dir(f))
			switch {
			case b.cmd == "forge-solo-migrate" && !b.sqlite:
				t.Errorf("BUILD-TAG-UMBREL-MIGRATE: %s builds the migrator without -tags sqlite", b.where)
			case b.cmd == "forge-solo-migrate":
			case db[service] == "DB_PATH" && !b.sqlite:
				t.Errorf("BUILD-TAG-UMBREL: %s builds %s without -tags sqlite, and docker-compose.yml gives the %s service DB_PATH", b.where, b.cmd, service)
			case db[service] == "DB_HOST" && b.sqlite:
				t.Errorf("BUILD-TAG-UMBREL-MATCH: %s builds %s with -tags sqlite, and docker-compose.yml gives the %s service DB_HOST", b.where, b.cmd, service)
			case db[service] != "DB_PATH" && db[service] != "DB_HOST":
				t.Errorf("BUILD-TAG-UMBREL-SERVICE: docker-compose.yml gives the %s service, which %s builds, no one database (%q)", service, b.where, db[service])
			}
		}
	}
	if !images["api"] || !images["stratum"] {
		t.Errorf("BUILD-TAG-UMBREL: the images no longer build the api and the stratum (%v)", images)
	}
}
