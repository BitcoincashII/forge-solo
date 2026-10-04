package forgesolo

import (
	"regexp"
	"testing"
)

// The migrator moves the data into the schema of the release it was built with, and a merge
// refuses a database a newer release wrote. Run beside a newer app, an older migrator would build
// a database that app has to fix up, or drop what it cannot map. So the migrate image is always
// the app's own version: the one the compose pins for the migrate service and for the postgres
// service, the version umbrel-app.yml gives the app, and the Windows installer's, which ships the
// same migrator. The digests follow at the release's re-pin; the tags must already agree on the
// release commit.
func TestMigratorIsTheAppVersion(t *testing.T) {
	m := manifestVersionRe.FindSubmatch(mustRead(t, "umbrel-app.yml"))
	if m == nil {
		t.Fatal("VERSION-LOCK: umbrel-app.yml has no version: field")
	}
	app := string(m[1])
	iss := regexp.MustCompile(`(?m)^\s*#define MyAppVersion "([^"]+)"`).FindSubmatch(mustRead(t, "windows/forge-solo.iss"))
	if iss == nil {
		t.Fatal("VERSION-LOCK-ISS: windows/forge-solo.iss defines no MyAppVersion")
	}
	if string(iss[1]) != app {
		t.Errorf("VERSION-LOCK-ISS: the Windows installer is %s, the app %s", iss[1], app)
	}
	tag := regexp.MustCompile(`^ghcr\.io/bitcoincashii/forge-solo-migrate:([^@]+)@sha256:[0-9a-f]{64}$`)
	svcs := sqliteCompose(t)
	for code, svc := range map[string]string{"VERSION-LOCK-MIGRATE": "migrate", "VERSION-LOCK-TOMBSTONE": "postgres"} {
		got := tag.FindStringSubmatch(svcs[svc].Image)
		if got == nil {
			t.Errorf("%s: the %s service runs %q, not the migrate image", code, svc, svcs[svc].Image)
		} else if got[1] != app {
			t.Errorf("%s: the %s service runs forge-solo-migrate %s, but the app is %s", code, svc, got[1], app)
		}
	}
}
