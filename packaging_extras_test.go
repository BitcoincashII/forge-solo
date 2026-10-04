package forgesolo

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Compose's "version" is obsolete: umbreld logged a warning about it on every compose action.
// Neither node keeps a wallet, so none can be created or used over RPC. Umbrel's backups skip the
// chains, which are copied live and downloaded again anyway, and the nodes' debug.log, which
// repeats their container logs and grows to tens of megabytes between restarts.
func TestComposeAndManifestHousekeeping(t *testing.T) {
	b, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Version  *string `yaml:"version"`
		Services map[string]struct {
			Command []string `yaml:"command"`
			Volumes []string `yaml:"volumes"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if c.Version != nil {
		t.Errorf("UMB1-NO-VERSION: docker-compose.yml still says version %q", *c.Version)
	}
	for _, node := range []string{"node", "node1175"} {
		found := false
		for _, arg := range c.Services[node].Command {
			found = found || arg == "-disablewallet"
		}
		if !found {
			t.Errorf("PKG4-NO-WALLET: %s starts without -disablewallet", node)
		}
	}

	m, err := os.ReadFile("umbrel-app.yml")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		BackupIgnore []string `yaml:"backupIgnore"`
	}
	if err := yaml.Unmarshal(m, &manifest); err != nil {
		t.Fatal(err)
	}
	mounted := map[string]bool{}
	for _, svc := range c.Services {
		for _, v := range svc.Volumes {
			if src, ok := strings.CutPrefix(strings.SplitN(v, ":", 2)[0], "${APP_DATA_DIR}/"); ok {
				mounted[src] = true
			}
		}
	}
	allowed := regexp.MustCompile(`^[-a-zA-Z0-9._/*]+$`) // umbreld skips anything else
	want := map[string]bool{
		"node/blocks": true, "node/chainstate": true, "node/debug.log": true,
		"node1175/blocks": true, "node1175/chainstate": true, "node1175/debug.log": true,
	}
	for _, p := range manifest.BackupIgnore {
		if !allowed.MatchString(p) || !mounted[strings.SplitN(p, "/", 2)[0]] {
			t.Errorf("PKG5-BACKUP-IGNORE: %q is not a path umbreld can apply to a mounted directory", p)
		}
		delete(want, p)
	}
	if len(want) > 0 {
		t.Errorf("PKG5-BACKUP-IGNORE: backupIgnore misses %v", want)
	}
}
