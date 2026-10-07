package forgesolo

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// A shippedBuild is one go build of the api, the stratum or the migrator in what makes a release.
type shippedBuild struct {
	where, cmd string // the file and line; the command built (api, stratum, forge-solo-migrate)
	tagged     bool   // built with -tags
}

var (
	goBuildCmd  = regexp.MustCompile(`"?\./cmd/([\w$-]+)"?`)
	buildTags   = regexp.MustCompile(`-tags[ =]`)
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
					out = append(out, shippedBuild{where: file + ":" + strconv.Itoa(n), cmd: c, tagged: buildTags.MatchString(line)})
				}
			}
		}
	}
	return out
}

// Forge Solo has one build. Up to 1.0.13 the services were built with a tag that chose SQLite, and
// the build without it looked for a PostgreSQL server that no platform runs; that build is gone,
// and with it the tag. Each shipped build is read here (the Windows release, the Linux tarballs,
// the Umbrel images) and names no tag, so that one place cannot keep building a program that
// differs from the others'.
func TestShippedBuildsAreTheOneBuild(t *testing.T) {
	for _, c := range []struct {
		code, file string
		want       []string
	}{
		{"ONE-BUILD-WINDOWS", ".github/workflows/release.yml", []string{"api", "stratum", "forge-solo-migrate"}},
		{"ONE-BUILD-LINUX", "scripts/linux/build-release.sh", []string{"api", "stratum"}},
	} {
		var built []string
		for _, b := range shippedBuilds(t, c.file) {
			built = append(built, b.cmd)
			if b.tagged {
				t.Errorf("%s: %s builds %s with a build tag", c.code, b.where, b.cmd)
			}
		}
		for _, w := range c.want {
			if !slices.Contains(built, w) {
				t.Errorf("%s: %s no longer builds %s (it builds %v)", c.code, c.file, w, built)
			}
		}
	}

	files, err := filepath.Glob("docker/*/Dockerfile")
	if err != nil || len(files) == 0 {
		t.Fatalf("no Dockerfiles: %v", err)
	}
	images := map[string]bool{}
	for _, f := range files {
		for _, b := range shippedBuilds(t, f) {
			images[b.cmd] = true
			if b.tagged {
				t.Errorf("ONE-BUILD-UMBREL: %s builds %s with a build tag", b.where, b.cmd)
			}
		}
	}
	for _, c := range shippedCmds {
		if !images[c] {
			t.Errorf("ONE-BUILD-UMBREL: the images no longer build %s (%v)", c, images)
		}
	}
}

// No file of the tree carries the sqlite constraint or names the tag: not a source, a workflow, a
// script, a Dockerfile or a README. The it tag of the move's end-to-end test and the seed1012 tag of
// 1.0.12's seed are the only build tags left.
func TestNoBuildTagSelectsADatabase(t *testing.T) {
	constraint := regexp.MustCompile(`(?m)^//go:build .*\bsqlite\b`)
	tagged := regexp.MustCompile(`-tags[ =]['"]?[\w ]*\bsqlite\b`)
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".go" && ext != ".yml" && ext != ".sh" && ext != ".md" && ext != ".py" && d.Name() != "Dockerfile" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if ext == ".go" && constraint.Match(b) {
			t.Errorf("ONE-BUILD-CONSTRAINT: %s has a build constraint on sqlite; there is one build", path)
		}
		if m := tagged.Find(b); m != nil {
			t.Errorf("ONE-BUILD-TAG: %s names %q; there is one build", path, m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
