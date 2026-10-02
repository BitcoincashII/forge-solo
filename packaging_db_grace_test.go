package forgesolo

import (
	"os"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// An install waits for the database to be healthy before it starts the api and stratum, and fails
// if it is not by start_period plus retries x interval: a slow first start must not fail it.
func TestTheDatabaseHasTimeToStart(t *testing.T) {
	b, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Services map[string]struct {
			Healthcheck struct {
				StartPeriod string `yaml:"start_period"`
			} `yaml:"healthcheck"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	d, err := time.ParseDuration(c.Services["postgres"].Healthcheck.StartPeriod)
	if err != nil || d < 5*time.Minute {
		t.Fatalf("PKG-DB-GRACE: the database gets %q to start before an install gives up, want at least 5 minutes", c.Services["postgres"].Healthcheck.StartPeriod)
	}
}
