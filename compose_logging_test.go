package forgesolo

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

// Every container's log is capped by the app, below the 100 MB umbrelOS 2.0 keeps for each
// container: an idle install logged 3.6 MB a day, a busy one tens of megabytes.
func TestEveryContainerLogIsCapped(t *testing.T) {
	b, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Services map[string]struct {
			Image   string `yaml:"image"`
			Logging struct {
				Driver  string            `yaml:"driver"`
				Options map[string]string `yaml:"options"`
			} `yaml:"logging"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	for name, s := range c.Services {
		if s.Image == "" {
			continue // app_proxy: Umbrel replaces it with its own gateway
		}
		if s.Logging.Driver != "json-file" || s.Logging.Options["max-size"] == "" || s.Logging.Options["max-file"] == "" {
			t.Errorf("service %s has no capped log (logging: json-file with max-size and max-file)", name)
		}
	}
}
