package forgesolo

import (
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// dependabotEntry is an entry of .github/dependabot.yml's updates, as far as the tests read it.
type dependabotEntry struct {
	Ecosystem   string                     `yaml:"package-ecosystem"`
	Directory   string                     `yaml:"directory"`
	Directories []string                   `yaml:"directories"`
	Groups      map[string]dependabotGroup `yaml:"groups"`
}

// dependabotGroup is a group of an entry: the dependencies it takes, and which of their updates.
type dependabotGroup struct {
	Patterns        []string `yaml:"patterns"`
	ExcludePatterns []string `yaml:"exclude-patterns"`
	UpdateTypes     []string `yaml:"update-types"`
}

func dependabotEntries(t *testing.T) []dependabotEntry {
	t.Helper()
	var c struct {
		Updates []dependabotEntry `yaml:"updates"`
	}
	if err := yaml.Unmarshal(mustRead(t, ".github/dependabot.yml"), &c); err != nil {
		t.Fatal(err)
	}
	return c.Updates
}

// Dependabot proposes updates for every Go module and every image this repo builds. It reads go.mod
// and Dockerfiles only in the directories it is given, so a module or image added later, like the
// Windows launcher (its own module, compiled into forge-solo.exe), is otherwise never updated.
func TestDependabotCoversEveryModuleAndImage(t *testing.T) {
	covered := map[string]bool{}
	for _, u := range dependabotEntries(t) {
		for _, d := range append([]string{u.Directory}, u.Directories...) {
			if d != "" {
				covered[u.Ecosystem+" "+d] = true
			}
		}
	}
	found := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" || d.Name() == "dist") {
			return fs.SkipDir
		}
		ecosystem := map[string]string{"go.mod": "gomod", "Dockerfile": "docker"}[d.Name()]
		if d.IsDir() || ecosystem == "" {
			return nil
		}
		found++
		dir := "/" + filepath.ToSlash(filepath.Dir(path))
		if dir == "/." {
			dir = "/"
		}
		if !covered[ecosystem+" "+dir] {
			t.Errorf("DEPENDABOT-COVERS: %s is not in a %s entry of .github/dependabot.yml, so nothing proposes its updates", path, ecosystem)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found < 2 {
		t.Fatalf("DEPENDABOT-COVERS: found %d go.mod and Dockerfile files; the walk is not reading the repo", found)
	}
}

// A push to main opened a pull request for every update in every folder: nine for 1.0.14. In each
// entry one group takes every dependency but those the entry's other groups take, so every update
// comes in its group's pull request, one for all the entry's folders. No group sets update-types:
// Dependabot leaves an update of another type, and a digest refresh (the same tag, rebuilt), out of
// such a group, and opens it alone in each folder. No dependency is in two groups: while one
// group's pull request is open, Dependabot holds back every dependency that group takes.
func TestDependabotGroupsEveryUpdate(t *testing.T) {
	for _, u := range dependabotEntries(t) {
		entry := strings.TrimSpace(u.Ecosystem + " " + u.Directory + " " + strings.Join(u.Directories, " "))
		var every []string
		taken := map[string]string{}
		for name, g := range u.Groups {
			if len(g.UpdateTypes) > 0 {
				t.Errorf("DEPENDABOT-GROUP-UPDATES: group %s of %s sets update-types %v: an update it leaves out comes alone, in each folder", name, entry, g.UpdateTypes)
			}
			if slices.Contains(g.Patterns, "*") {
				every = append(every, name)
				continue
			}
			for _, p := range g.Patterns {
				if other, ok := taken[p]; ok {
					t.Errorf("DEPENDABOT-GROUP-TWICE: groups %s and %s of %s both take %s", other, name, entry, p)
				}
				taken[p] = name
			}
		}
		if len(every) != 1 {
			t.Errorf("DEPENDABOT-GROUP-ALL: %s has %d groups that take every dependency %v, want one", entry, len(every), every)
			continue
		}
		left, others := slices.Sorted(slices.Values(u.Groups[every[0]].ExcludePatterns)), slices.Sorted(maps.Keys(taken))
		if !slices.Equal(left, others) {
			t.Errorf("DEPENDABOT-GROUP-ALL: group %s of %s leaves out %v and the entry's other groups take %v; they must be the same",
				every[0], entry, left, others)
		}
	}
}
