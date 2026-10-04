package forgesolo

import (
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// On Umbrel the api and the stratum keep their data in one SQLite file, db/forgesolo.db, which
// both open. Before they start, the migrate service moves the PostgreSQL data of 1.0.12 and
// earlier into it, once. The postgres service is kept by name only, so that an update made while
// 1.0.12's database server still runs stops that server before the move starts.

// sqliteService is the part of a compose service these tests read.
type sqliteService struct {
	Image           string    `yaml:"image"`
	Command         []string  `yaml:"command"`
	Restart         string    `yaml:"restart"`
	NetworkMode     string    `yaml:"network_mode"`
	Networks        []string  `yaml:"networks"`
	User            string    `yaml:"user"`
	ReadOnly        bool      `yaml:"read_only"`
	SecurityOpt     []string  `yaml:"security_opt"`
	CapDrop         []string  `yaml:"cap_drop"`
	CapAdd          []string  `yaml:"cap_add"`
	StopGracePeriod string    `yaml:"stop_grace_period"`
	Volumes         []string  `yaml:"volumes"`
	Ports           []string  `yaml:"ports"`
	Environment     yaml.Node `yaml:"environment"`
	DependsOn       yaml.Node `yaml:"depends_on"`
	Healthcheck     yaml.Node `yaml:"healthcheck"`
}

func sqliteCompose(t *testing.T) map[string]sqliteService {
	t.Helper()
	var c struct {
		Services map[string]sqliteService `yaml:"services"`
	}
	if err := yaml.Unmarshal(mustRead(t, "docker-compose.yml"), &c); err != nil {
		t.Fatal(err)
	}
	return c.Services
}

// env is a service's environment as KEY -> value, from either compose syntax.
func (s sqliteService) env() map[string]string {
	out := map[string]string{}
	switch s.Environment.Kind {
	case yaml.SequenceNode:
		for _, n := range s.Environment.Content {
			k, v, _ := strings.Cut(n.Value, "=")
			out[k] = v
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(s.Environment.Content); i += 2 {
			out[s.Environment.Content[i].Value] = s.Environment.Content[i+1].Value
		}
	}
	return out
}

// dependsOn is a service's depends_on as service -> condition ("service_started" for the list form).
func (s sqliteService) dependsOn(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	switch s.DependsOn.Kind {
	case yaml.SequenceNode:
		for _, n := range s.DependsOn.Content {
			out[n.Value] = "service_started"
		}
	case yaml.MappingNode:
		var m map[string]struct {
			Condition string `yaml:"condition"`
		}
		if err := s.DependsOn.Decode(&m); err != nil {
			t.Fatal(err)
		}
		for k, v := range m {
			out[k] = v.Condition
		}
	}
	return out
}

var migrateImageRe = regexp.MustCompile(`^ghcr\.io/bitcoincashii/forge-solo-migrate:[0-9.]+@sha256:[0-9a-f]{64}$`)

// The postgres service does nothing and exits, and nothing else: no data, no network, no
// environment (the old server's password is no longer handed to anything), no privilege.
func TestSQLiteComposeTombstone(t *testing.T) {
	svcs := sqliteCompose(t)
	s, ok := svcs["postgres"]
	if !ok {
		t.Fatal("SQL-COMPOSE-TOMBSTONE: there is no postgres service: an update made while 1.0.12 runs would leave its database server running beside the move")
	}
	if !migrateImageRe.MatchString(s.Image) || s.Image != svcs["migrate"].Image {
		t.Errorf("SQL-COMPOSE-TOMBSTONE-IMAGE: postgres runs %q, not the migrate service's image %q", s.Image, svcs["migrate"].Image)
	}
	if !slices.Equal(s.Command, []string{"noop"}) || s.Restart != "no" {
		t.Errorf("SQL-COMPOSE-TOMBSTONE-ONESHOT: postgres runs %q with restart %q, want [noop] and \"no\"", s.Command, s.Restart)
	}
	if s.NetworkMode != "none" || s.Networks != nil || s.Ports != nil {
		t.Errorf("SQL-COMPOSE-TOMBSTONE-NETWORK: postgres has network_mode %q, networks %v, ports %v; want none and no others", s.NetworkMode, s.Networks, s.Ports)
	}
	if s.User != "65534:65534" || !s.ReadOnly || !slices.Equal(s.CapDrop, []string{"ALL"}) || s.CapAdd != nil ||
		!slices.Contains(s.SecurityOpt, "no-new-privileges:true") {
		t.Errorf("SQL-COMPOSE-TOMBSTONE-PRIVILEGE: postgres runs as %q, read_only %v, cap_drop %v, cap_add %v, security_opt %v",
			s.User, s.ReadOnly, s.CapDrop, s.CapAdd, s.SecurityOpt)
	}
	if s.Volumes != nil || s.Environment.Kind != 0 || s.DependsOn.Kind != 0 || s.Healthcheck.Kind != 0 {
		t.Errorf("SQL-COMPOSE-TOMBSTONE-EMPTY: postgres has volumes %v or an environment, depends_on or healthcheck", s.Volumes)
	}
	if s.StopGracePeriod != "1m" {
		t.Errorf("SQL-COMPOSE-TOMBSTONE-GRACE: postgres has stop_grace_period %q, want 1m, the old server's", s.StopGracePeriod)
	}
}

// The migrate service runs once per start, after the tombstone has exited (so after any 1.0.12
// database server is stopped), with the old data and the database's folder and only the
// privileges the move needs.
func TestSQLiteComposeMigrate(t *testing.T) {
	s, ok := sqliteCompose(t)["migrate"]
	if !ok {
		t.Fatal("SQL-COMPOSE-MIGRATE: there is no migrate service")
	}
	if !migrateImageRe.MatchString(s.Image) {
		t.Errorf("SQL-COMPOSE-MIGRATE-IMAGE: migrate runs %q", s.Image)
	}
	if want := []string{"run", "--db=/data/forgesolo.db", "--pgdata=/pg", "--owner=10001:10001"}; !slices.Equal(s.Command, want) {
		t.Errorf("SQL-COMPOSE-MIGRATE-COMMAND: migrate runs %q, want %q", s.Command, want)
	}
	if s.Restart != "no" || s.Healthcheck.Kind != 0 {
		t.Errorf("SQL-COMPOSE-MIGRATE-ONESHOT: migrate has restart %q or a healthcheck; it runs once per start and exits", s.Restart)
	}
	if s.NetworkMode != "none" || s.Networks != nil || s.Ports != nil {
		t.Errorf("SQL-COMPOSE-MIGRATE-NETWORK: migrate has network_mode %q, networks %v, ports %v; want none and no others", s.NetworkMode, s.Networks, s.Ports)
	}
	caps := slices.Clone(s.CapAdd)
	slices.Sort(caps)
	if want := []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "KILL", "SETGID", "SETUID"}; !slices.Equal(caps, want) ||
		!slices.Equal(s.CapDrop, []string{"ALL"}) || !slices.Contains(s.SecurityOpt, "no-new-privileges:true") || s.User != "" {
		t.Errorf("SQL-COMPOSE-MIGRATE-CAPS: migrate runs as %q with cap_drop %v, cap_add %v, security_opt %v; want root with exactly %v",
			s.User, s.CapDrop, s.CapAdd, s.SecurityOpt, want)
	}
	if want := []string{"${APP_DATA_DIR}/postgres:/pg", "${APP_DATA_DIR}/db:/data"}; !slices.Equal(s.Volumes, want) {
		t.Errorf("SQL-COMPOSE-MIGRATE-VOLUMES: migrate mounts %v, want %v", s.Volumes, want)
	}
	if s.Environment.Kind != 0 {
		t.Error("SQL-COMPOSE-MIGRATE-ENV: migrate has an environment; it needs none (its server listens on a socket of its own)")
	}
	if d := s.dependsOn(t); len(d) != 1 || d["postgres"] != "service_completed_successfully" {
		t.Errorf("SQL-COMPOSE-MIGRATE-AFTER-TOMBSTONE: migrate depends on %v, want only postgres: service_completed_successfully", d)
	}
	if s.StopGracePeriod != "2m" {
		t.Errorf("SQL-COMPOSE-MIGRATE-GRACE: migrate has stop_grace_period %q, want 2m", s.StopGracePeriod)
	}
}

// The api and the stratum open the same file, in a folder (not a bind-mounted file: SQLite keeps
// its -wal, -shm and in-use files beside it, and the move replaces it by a rename), as the user
// that folder belongs to, and only once the move has finished. Nothing addresses a database server.
func TestSQLiteComposeAppDatabase(t *testing.T) {
	svcs := sqliteCompose(t)
	var paths []string
	for _, name := range []string{"api", "stratum"} {
		s := svcs[name]
		if s.User != "10001:10001" {
			t.Errorf("SQL-COMPOSE-APP-USER: %s runs as %q, want 10001:10001, who owns the database's folder", name, s.User)
		}
		if d := s.dependsOn(t); d["migrate"] != "service_completed_successfully" || d["postgres"] != "" {
			t.Errorf("SQL-COMPOSE-APP-WAITS: %s depends on %v, want migrate: service_completed_successfully and not postgres", name, d)
		}
		dbPath := s.env()["DB_PATH"]
		paths = append(paths, dbPath)
		mounted := false
		for _, v := range s.Volumes {
			host, ctr, _ := strings.Cut(v, ":")
			if ctr == path.Dir(dbPath) && host == "${APP_DATA_DIR}/db" {
				mounted = true
			}
			if strings.HasSuffix(host, ".db") || strings.HasSuffix(ctr, ".db") {
				t.Errorf("SQL-COMPOSE-DB-DIR: %s mounts the database file itself (%s): mount its folder", name, v)
			}
		}
		if !mounted {
			t.Errorf("SQL-COMPOSE-DB-DIR: %s does not mount ${APP_DATA_DIR}/db at the folder of its DB_PATH %q (volumes %v)", name, dbPath, s.Volumes)
		}
		for k := range s.env() {
			if strings.HasPrefix(k, "DB_") && k != "DB_PATH" {
				t.Errorf("SQL-COMPOSE-NO-SERVER: %s still gets %s", name, k)
			}
		}
	}
	if paths[0] != "/data/forgesolo.db" || paths[1] != paths[0] {
		t.Errorf("SQL-COMPOSE-DB-PATH: the api's DB_PATH is %q and the stratum's %q, want both /data/forgesolo.db", paths[0], paths[1])
	}
	if strings.Contains(string(mustRead(t, "docker-compose.yml")), "DB_HOST") {
		t.Error("SQL-COMPOSE-NO-SERVER: docker-compose.yml still names DB_HOST")
	}
}

// The api and stratum images are the SQLite build, and their user's group is 10001 too, the group
// the database's folder and files belong to.
func TestSQLiteAppImages(t *testing.T) {
	for _, svc := range []string{"api", "stratum"} {
		f := "docker/" + svc + "/Dockerfile"
		src := string(mustRead(t, f))
		if !regexp.MustCompile(`go build -tags sqlite [^\n]*\./cmd/` + svc + `\n`).MatchString(src) {
			t.Errorf("SQL-DOCKERFILE-SQLITE: %s does not build ./cmd/%s with -tags sqlite", f, svc)
		}
		if !strings.Contains(src, "groupadd --system --gid 10001 forge") || !strings.Contains(src, "useradd --system --uid 10001 --gid 10001 ") {
			t.Errorf("SQL-DOCKERFILE-GID: %s does not make its user forge with uid and gid 10001", f)
		}
	}
}
