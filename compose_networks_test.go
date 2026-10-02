package forgesolo

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

// The app's containers talk on a network of their own. On Umbrel every app's containers share
// umbrel_main_network, so anything there is reachable from every other app's containers and its
// traffic crosses that shared segment. Only web joins it, so umbrelOS can reach the dashboard.
func TestOnlyTheDashboardIsOnUmbrelsSharedNetwork(t *testing.T) {
	b, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Networks map[string]any `yaml:"networks"`
		Services map[string]struct {
			Networks []string `yaml:"networks"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Networks["forge"]; !ok {
		t.Fatal("NET-PRIVATE-DEFINED: docker-compose.yml defines no forge network")
	}
	for name, svc := range c.Services {
		switch name {
		case "app_proxy":
			if svc.Networks != nil {
				t.Errorf("NET-APP-PROXY: app_proxy names networks %v; umbrelOS places it", svc.Networks)
			}
		case "web":
			if len(svc.Networks) != 2 || svc.Networks[0] != "default" || svc.Networks[1] != "forge" {
				t.Errorf("NET-WEB: web is on %v, want [default forge]", svc.Networks)
			}
		default:
			if len(svc.Networks) != 1 || svc.Networks[0] != "forge" {
				t.Errorf("NET-PRIVATE: %s is on %v, want only [forge] (default is Umbrel's shared network)", name, svc.Networks)
			}
		}
	}
}
