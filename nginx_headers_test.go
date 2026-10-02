package forgesolo

import (
	"os"
	"strings"
	"testing"
)

// Every response from the dashboard's nginx carries the security headers. nginx drops the server's
// add_header lines in any location that has its own, so each such location must include the
// snippet again.
func TestEveryDashboardResponseCarriesTheSecurityHeaders(t *testing.T) {
	const include = "include /etc/nginx/snippets/security-headers.conf;"
	snippet, err := os.ReadFile("docker/web/security-headers.conf")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{`X-Frame-Options "DENY" always;`, `Content-Security-Policy "frame-ancestors 'none'" always;`,
		`X-Content-Type-Options "nosniff" always;`, `Referrer-Policy "no-referrer" always;`} {
		if !strings.Contains(string(snippet), "add_header "+h) {
			t.Errorf("HDR-SNIPPET: security-headers.conf lacks add_header %s", h)
		}
	}
	conf, err := os.ReadFile("docker/web/nginx.conf")
	if err != nil {
		t.Fatal(err)
	}
	depth, serverIncludes := 0, false
	for _, line := range strings.Split(string(conf), "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "#") {
			continue
		}
		if depth == 1 && l == include {
			serverIncludes = true
		}
		if strings.HasPrefix(l, "location") && strings.Contains(l, "add_header") && !strings.Contains(l, include) {
			t.Errorf("HDR-LOCATION: %q adds a header but does not include the security headers", l)
		}
		depth += strings.Count(l, "{") - strings.Count(l, "}")
	}
	if !serverIncludes {
		t.Error("HDR-SERVER: the server block does not include the security headers")
	}
	if !strings.Contains(string(conf), "server_tokens off;") {
		t.Error("HDR-VERSION: nginx still names its version in every response")
	}
	df, _ := os.ReadFile("docker/web/Dockerfile")
	if !strings.Contains(string(df), "COPY docker/web/security-headers.conf /etc/nginx/snippets/security-headers.conf") {
		t.Error("HDR-IMAGE: the image does not carry security-headers.conf where nginx.conf includes it from")
	}
}
