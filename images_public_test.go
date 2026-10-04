package forgesolo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// scripts/check-images-public.sh is the release's check that an Umbrel can pull every image the
// compose pins without a login. It runs here against a registry on this machine that answers as
// GHCR does: 401 with a Bearer challenge, an anonymous token for a public package, and 403 for a
// private or missing one, the same answer for both.
func TestImagesPublicCheck(t *testing.T) {
	for _, tool := range []string{"bash", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("IMAGES-PUBLIC-HARNESS: this test needs %s: %v", tool, err)
		}
	}
	digest := func(c string) string { return "sha256:" + strings.Repeat(c, 64) }
	// A real digest can start with zeros.
	leadingZeros := "sha256:" + strings.Repeat("0", 12) + strings.Repeat("b", 52)

	public := map[string]string{ // repository -> the digest it has
		"acme/one": digest("a"),
		"acme/two": leadingZeros,
	}
	open := map[string]string{"library/open": digest("c")} // served with no token at all
	refused := "acme/refused"                              // gets a token, and then no manifest

	// The real compose's six images, as if each were public. Until CI re-pins it, a release commit
	// pins zeros for an image new in that release; here that image has a digest of its own.
	real, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	realCompose := strings.ReplaceAll(string(real), "@"+digest("0"), "@"+digest("f"))
	pinned := composeImageRe.FindAllStringSubmatch(realCompose, -1)
	if len(pinned) != 6 {
		t.Fatalf("IMAGES-PUBLIC-HARNESS: %d pinned images in docker-compose.yml, want 6", len(pinned))
	}
	for _, m := range pinned {
		public["bitcoincashii/"+m[1]] = "sha256:" + m[3]
	}

	manifest := regexp.MustCompile(`^/v2/(.+)/manifests/(sha256:[0-9a-f]{64})$`)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			q := r.URL.Query()
			repo := strings.TrimSuffix(strings.TrimPrefix(q.Get("scope"), "repository:"), ":pull")
			if _, ok := public[repo]; (ok || repo == refused) && q.Get("service") == "fake" && r.Header.Get("Authorization") == "" {
				fmt.Fprintf(w, `{"token":"anon-%s"}`, repo)
				return
			}
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"errors":[{"code":"DENIED","message":"requested access to the resource is denied"}]}`)
			return
		}
		m := manifest.FindStringSubmatch(r.URL.Path)
		// A multi-platform image's digest names an index, served only to a client that accepts one.
		if m == nil || r.Method != http.MethodHead || !strings.Contains(r.Header.Get("Accept"), "application/vnd.oci.image.index.v1+json") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		repo, d := m[1], m[2]
		if want, ok := open[repo]; ok {
			if d != want {
				w.WriteHeader(http.StatusNotFound)
			}
			return
		}
		if r.Header.Get("Authorization") != "Bearer anon-"+repo {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake",scope="repository:%s:pull"`, srv.URL, repo))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if repo == refused {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if public[repo] != d {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	check := func(compose string) (string, bool) {
		t.Helper()
		f := filepath.Join(t.TempDir(), "docker-compose.yml")
		if err := os.WriteFile(f, []byte(compose), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("bash", "scripts/check-images-public.sh", f).CombinedOutput()
		if _, ok := err.(*exec.ExitError); err != nil && !ok {
			t.Fatal(err)
		}
		return string(out), err == nil
	}
	composeOf := func(images ...string) string {
		var b strings.Builder
		b.WriteString("services:\n")
		for i, im := range images {
			fmt.Fprintf(&b, "  s%d:\n    image: %s\n    restart: unless-stopped\n", i, im)
		}
		return b.String()
	}
	one := host + "/acme/one:1.0.13@" + digest("a")
	two := host + "/acme/two:1.0.13@" + leadingZeros

	if out, ok := check(composeOf(one, two, host+"/library/open@"+digest("c"))); !ok || strings.Count(out, "ok   ") != 3 {
		t.Fatalf("IMAGES-PUBLIC-OK: three images anyone can pull did not all pass:\n%s", out)
	}
	if out, ok := check(strings.ReplaceAll(realCompose, "ghcr.io/", host+"/")); !ok || strings.Count(out, "ok   ") != 6 {
		t.Fatalf("IMAGES-PUBLIC-REAL-COMPOSE: the six images of docker-compose.yml, all public, did not all pass:\n%s", out)
	}
	for _, tc := range []struct{ code, image, want string }{
		{"IMAGES-PUBLIC-PRIVATE", host + "/acme/private:1.0.13@" + digest("d"), "needs a login"},
		{"IMAGES-PUBLIC-MISSING", host + "/acme/one:1.0.13@" + digest("e"), "has no such digest"},
		{"IMAGES-PUBLIC-TOKEN-REFUSED", host + "/" + refused + ":1.0.13@" + digest("a"), "answers 403"},
		// The release commit, before CI's re-pin: even with the package public, zeros never pull.
		{"IMAGES-PUBLIC-PLACEHOLDER", host + "/acme/one:1.0.13@" + digest("0"), "zeros, not a digest CI published: pull main once CI's re-pin commit is on it"},
		{"IMAGES-PUBLIC-UNPINNED", host + "/acme/two:1.0.13", "not a registry/name[:tag]@sha256:digest"},
		{"IMAGES-PUBLIC-UNREACHABLE", "127.0.0.1:1/acme/one:1.0.13@" + digest("a"), "cannot be reached"},
		{"IMAGES-PUBLIC-NO-HOST", "timescale/timescaledb:2.17.2-pg16@" + digest("a"), "names no registry host"},
	} {
		out, ok := check(composeOf(one, tc.image, two))
		said := false
		for _, line := range strings.Split(out, "\n") {
			said = said || strings.HasPrefix(line, "FAIL "+tc.image+": ") && strings.Contains(line, tc.want)
		}
		if ok || !said {
			t.Errorf("%s: a compose pinning %s passed (%v) or did not say %q:\n%s", tc.code, tc.image, ok, tc.want, out)
		}
	}
	if out, ok := check("services:\n  app_proxy:\n    environment: {}\n"); ok {
		t.Errorf("IMAGES-PUBLIC-EMPTY: a compose with no images passed:\n%s", out)
	}
}

// Tests runs the check on every push, and on the main the release's re-pin starts it on, in a job
// of its own whose failure fails the run.
func TestImagesPublicCheckRunsInCI(t *testing.T) {
	b, err := os.ReadFile(".github/workflows/test.yml")
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		Jobs map[string]struct {
			ContinueOnError any `yaml:"continue-on-error"`
			Steps           []struct {
				Run             string `yaml:"run"`
				ContinueOnError any    `yaml:"continue-on-error"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	for _, j := range w.Jobs {
		for _, s := range j.Steps {
			if strings.TrimSpace(s.Run) == "scripts/check-images-public.sh" && j.ContinueOnError == nil && s.ContinueOnError == nil {
				return
			}
		}
	}
	t.Error("IMAGES-PUBLIC-IN-CI: no job in test.yml runs scripts/check-images-public.sh so that its failure fails the run")
}
