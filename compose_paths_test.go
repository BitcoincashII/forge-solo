package forgesolo

// Every host path the compose file mounts must be absolute (in practice, under ${APP_DATA_DIR}).
//
// Umbrel runs compose with its own fragment file first, and compose resolves a relative host path
// from the first file's directory, not this one's. "./init-db.sql" therefore named a file in
// umbreld's own directory that does not exist: Docker created an empty directory in its place, and
// postgres failed every fresh install's init script with "could not read from input file: Is a
// directory". Nothing else showed it, because the Go code creates the schema too.

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestComposeMountsNoRelativeHostPaths(t *testing.T) {
	src, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			Volumes []yaml.Node `yaml:"volumes"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(src, &compose); err != nil {
		t.Fatal(err)
	}
	mounts := 0
	for name, svc := range compose.Services {
		for _, v := range svc.Volumes {
			var host string
			switch v.Kind {
			case yaml.ScalarNode: // short syntax, host:container[:mode]
				host, _, _ = strings.Cut(v.Value, ":")
			case yaml.MappingNode: // long syntax
				var m struct{ Type, Source string }
				if err := v.Decode(&m); err != nil {
					t.Fatal(err)
				}
				if m.Type != "bind" {
					continue
				}
				host = m.Source
			}
			if !strings.Contains(host, "/") {
				continue // a named volume
			}
			mounts++
			if !strings.HasPrefix(host, "/") && !strings.HasPrefix(host, "${") {
				t.Errorf("service %s mounts the relative host path %q: on Umbrel it resolves against umbreld's "+
					"own directory. Use ${APP_DATA_DIR}/...", name, host)
			}
		}
	}
	if mounts == 0 {
		t.Fatal("found no host mounts at all: has the compose layout changed?")
	}
}
