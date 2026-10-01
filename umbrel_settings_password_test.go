package forgesolo

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// On Umbrel the api takes no settings change without the app's password (it refuses them all
// when it has none), and the owner can only type a password umbrelOS shows them. The compose file
// must hand the api APP_PASSWORD, and the manifest must make umbrelOS show that same password.
func TestUmbrelSettingsPasswordIsWiredAndShown(t *testing.T) {
	b, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	// A service lists its environment either as KEY=value lines or as a map.
	var c struct {
		Services map[string]struct {
			Environment yaml.Node `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	env := func(n yaml.Node) map[string][]string {
		out := map[string][]string{}
		switch n.Kind {
		case yaml.SequenceNode:
			for _, e := range n.Content {
				k, v, _ := strings.Cut(e.Value, "=")
				out[k] = append(out[k], v)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				out[n.Content[i].Value] = append(out[n.Content[i].Value], n.Content[i+1].Value)
			}
		}
		return out
	}
	if got := env(c.Services["api"].Environment)["SETTINGS_PASSWORD"]; len(got) != 1 || got[0] != "${APP_PASSWORD}" {
		t.Errorf("UMBREL-PW-COMPOSE: the api service must have SETTINGS_PASSWORD=${APP_PASSWORD} once, has %q", got)
	}
	for name, s := range c.Services {
		if v, ok := env(s.Environment)["SETTINGS_PASSWORD"]; ok && name != "api" {
			t.Errorf("UMBREL-PW-ELSEWHERE: %s gets SETTINGS_PASSWORD=%q; only the api needs the password", name, v)
		}
	}

	m, err := os.ReadFile("umbrel-app.yml")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		DeterministicPassword bool   `yaml:"deterministicPassword"`
		DefaultPassword       string `yaml:"defaultPassword"`
	}
	if err := yaml.Unmarshal(m, &manifest); err != nil {
		t.Fatal(err)
	}
	if !manifest.DeterministicPassword {
		t.Error("UMBREL-PW-SHOWN: umbrel-app.yml needs deterministicPassword: true, or umbrelOS never shows the password Settings asks for")
	}
	// A fixed defaultPassword would be shown instead of nothing, but it is not the one the api checks.
	if manifest.DefaultPassword != "" {
		t.Errorf("UMBREL-PW-FIXED: defaultPassword %q is not the password the api checks", manifest.DefaultPassword)
	}
}
