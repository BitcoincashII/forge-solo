package forgesolo

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Dependabot proposes updates for every Go module and every image this repo builds. It reads go.mod
// and Dockerfiles only in the directories it is given, so a module or image added later, like the
// Windows launcher (its own module, compiled into forge-solo.exe), is otherwise never updated.
func TestDependabotCoversEveryModuleAndImage(t *testing.T) {
	b, err := os.ReadFile(".github/dependabot.yml")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Updates []struct {
			Ecosystem   string   `yaml:"package-ecosystem"`
			Directory   string   `yaml:"directory"`
			Directories []string `yaml:"directories"`
		} `yaml:"updates"`
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	covered := map[string]bool{}
	for _, u := range c.Updates {
		for _, d := range append([]string{u.Directory}, u.Directories...) {
			if d != "" {
				covered[u.Ecosystem+" "+d] = true
			}
		}
	}
	found := 0
	err = filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
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
