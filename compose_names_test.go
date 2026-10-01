package forgesolo

// The services must address each other by container name, never by bare service name.
//
// Every Umbrel app joins the one shared umbrel_main_network, where "postgres", "api", "node" and
// the like are also aliases of other apps' services (BTCPay Server and Immich have a postgres,
// mempool and LocalAI an api). A bare name then resolves to both apps' containers: in review, 22
// of 40 fresh database connections went to another app's postgres. umbreld names every container
// <app id>_<service>_1, which is unique on the network.

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestServicesAddressEachOtherByContainerName(t *testing.T) {
	src, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			Environment yaml.Node `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(src, &compose); err != nil {
		t.Fatal(err)
	}
	var names []string
	for name := range compose.Services {
		names = append(names, regexp.QuoteMeta(name))
	}
	// A value that is, or contains as a host, a bare service name: "postgres", "http://api:8080",
	// "tcp://node:28332".
	bare := regexp.MustCompile(`(^|//)(` + strings.Join(names, "|") + `)(:[0-9]+)?(/|$)`)
	checked := 0
	for svc, s := range compose.Services {
		var values []string
		switch s.Environment.Kind {
		case yaml.SequenceNode: // - KEY=value
			for _, n := range s.Environment.Content {
				if _, v, ok := strings.Cut(n.Value, "="); ok {
					values = append(values, v)
				}
			}
		case yaml.MappingNode: // KEY: value
			for i := 1; i < len(s.Environment.Content); i += 2 {
				values = append(values, s.Environment.Content[i].Value)
			}
		}
		for _, v := range values {
			checked++
			if bare.MatchString(v) {
				t.Errorf("service %s: %q addresses a service by bare name; use bch2-apps-forge-solo_<service>_1", svc, v)
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no environment values at all: has the compose layout changed?")
	}

	nginx, err := os.ReadFile("docker/web/nginx.conf")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(nginx), "proxy_pass http://${API_UPSTREAM};") {
		t.Error("docker/web/nginx.conf must proxy to ${API_UPSTREAM}, which the compose sets to the api container's name")
	}
	if !strings.Contains(string(src), "API_UPSTREAM=bch2-apps-forge-solo_api_1:8080") {
		t.Error("docker-compose.yml must set API_UPSTREAM for the web service")
	}
}
