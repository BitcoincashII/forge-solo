package forgesolo

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// gwCILines is a step's script as its commands: each line trimmed, without blank lines and
// comments, which are not run.
func gwCILines(run string) []string {
	var out []string
	for _, l := range strings.Split(run, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// gwCIStep is the named step of a job, with its place among the job's steps, failing the test if
// there is none.
func gwCIStep(t *testing.T, w workflow, job, name string) (at int, dir, run string, env map[string]any) {
	t.Helper()
	for i, s := range w.Jobs[job].Steps {
		if s.Name == name {
			return i, s.WorkingDirectory, s.Run, s.Env
		}
	}
	t.Fatalf("GW-CI-STEP: job %s has no step %q", job, name)
	return 0, "", "", nil
}

// gwCIHas checks that the script runs each command, as a line of its own, in the order given.
func gwCIHas(t *testing.T, code, run string, want ...string) {
	t.Helper()
	lines, next := gwCILines(run), 0
	for _, w := range want {
		found := -1
		for i := next; i < len(lines); i++ {
			if lines[i] == w {
				found = i
				break
			}
		}
		if found < 0 {
			t.Errorf("%s: the step does not run, in its place:\n%s", code, w)
			continue
		}
		next = found + 1
	}
}

// gwCIUnitStaticcheck is the staticcheck version the unit job pins.
func gwCIUnitStaticcheck(t *testing.T, w workflow) string {
	t.Helper()
	pin := regexp.MustCompile(`(?m)^\s*go install honnef\.co/go/tools/cmd/staticcheck@(v[0-9.]+)$`)
	for _, s := range w.Jobs["unit"].Steps {
		if m := pin.FindStringSubmatch(s.Run); m != nil {
			return m[1]
		}
	}
	t.Fatal("GW-CI-TRAY: the unit job installs no pinned staticcheck")
	return ""
}

// Forge Gateway's tray app is a Go module of its own, like Forge Solo's launcher, so the unit job's
// ./... never reaches it: the Windows job vets it and staticchecks it as Windows code, at the
// unit job's staticcheck, builds it as the release does (no console), tests it with the race
// detector, and compiles its tests for Windows.
func TestGatewayCITray(t *testing.T) {
	w := loadWorkflow(t, ".github/workflows/test.yml")
	_, dir, run, _ := gwCIStep(t, w, "windows", "Vet, staticcheck, build and test Forge Gateway's tray app")
	if dir != "windows/gateway/launcher" {
		t.Errorf("GW-CI-TRAY-DIR: the tray app's step runs in %q, not windows/gateway/launcher", dir)
	}
	gwCIHas(t, "GW-CI-TRAY", run,
		"GOOS=windows GOARCH=amd64 go vet ./...",
		"go install honnef.co/go/tools/cmd/staticcheck@"+gwCIUnitStaticcheck(t, w),
		`GOOS=windows GOARCH=amd64 "$(go env GOPATH)/bin/staticcheck" ./...`,
		`CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-H=windowsgui -s -w" -o /tmp/forge-gateway-tray.exe .`,
		"go test -race -count=1 ./...",
		"GOOS=windows GOARCH=amd64 go test -c -o /tmp/gateway-tray-test.exe .",
	)
}

// forge-gateway.exe's Windows-only files (the service) are vetted, staticchecked, built and their
// tests compiled for Windows, after the tray app's step, whose staticcheck it uses; and the gateway
// and the pool gateway it shares with Forge Solo are tested with the race detector.
func TestGatewayCIGateway(t *testing.T) {
	w := loadWorkflow(t, ".github/workflows/test.yml")
	tray, _, _, _ := gwCIStep(t, w, "windows", "Vet, staticcheck, build and test Forge Gateway's tray app")
	at, dir, run, _ := gwCIStep(t, w, "windows", "Vet, build and test Forge Gateway for Windows")
	if at < tray || dir != "" {
		t.Errorf("GW-CI-GATEWAY-ORDER: the gateway's step is step %d in %q, the tray app's (which installs staticcheck) step %d", at, dir, tray)
	}
	gwCIHas(t, "GW-CI-GATEWAY", run,
		"GOOS=windows GOARCH=amd64 go vet ./cmd/forge-gateway",
		`GOOS=windows GOARCH=amd64 "$(go env GOPATH)/bin/staticcheck" ./cmd/forge-gateway`,
		"CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /dev/null ./cmd/forge-gateway",
		"CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -c -o /dev/null ./cmd/forge-gateway",
		"go test -race -count=1 ./cmd/forge-gateway ./internal/tidesgw",
	)
}

// Forge Gateway's installer script is compiled in the Windows job, with placeholders for its
// programs, in the image the release uses and with no network, and both programs must be in it.
func TestGatewayCIISS(t *testing.T) {
	w := loadWorkflow(t, ".github/workflows/test.yml")
	_, _, run, env := gwCIStep(t, w, "windows", "Forge Gateway's installer script compiles")
	release, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^  INNOSETUP_IMAGE: '([^']+)'$`).FindSubmatch(release)
	if m == nil || fmt.Sprint(env["INNOSETUP_IMAGE"]) != string(m[1]) {
		t.Errorf("GW-CI-ISS-IMAGE: the step runs %v, not release.yml's INNOSETUP_IMAGE", env["INNOSETUP_IMAGE"])
	}
	gwCIHas(t, "GW-CI-ISS", run,
		"mkdir -p windows/gateway/bin",
		": > windows/gateway/bin/forge-gateway.exe; : > windows/gateway/bin/forge-gateway-tray.exe; : > windows/gateway/bin/LICENSE.txt",
		"chmod a+w windows/gateway",
		`docker run --rm --network none -v "$PWD":/work "$INNOSETUP_IMAGE" /DMyAppVersion=0.0.0-check windows/gateway/forge-gateway.iss | tee /tmp/iscc-gw.log`,
		"test -f windows/gateway/ForgeGateway-Setup-0.0.0-check.exe",
		`grep -q 'bin\\forge-gateway.exe' /tmp/iscc-gw.log || { echo "::error::forge-gateway.exe is not in Forge Gateway's installer"; exit 1; }`,
		`grep -q 'bin\\forge-gateway-tray.exe' /tmp/iscc-gw.log || { echo "::error::the tray app is not in Forge Gateway's installer"; exit 1; }`,
	)
}

// On Windows itself, every test of the gateway and of its tray app runs, and none may skip: a
// skipped test passes nothing.
func TestGatewayCINative(t *testing.T) {
	w := loadWorkflow(t, ".github/workflows/test.yml")
	_, dir, run, _ := gwCIStep(t, w, "windows-native", "Forge Gateway and its tray app, on Windows")
	if dir != "" {
		t.Errorf("GW-CI-NATIVE-DIR: the step runs in %q, not the repository's root", dir)
	}
	gwCIHas(t, "GW-CI-NATIVE", run,
		`go test -count=1 -v ./cmd/forge-gateway | tee "$RUNNER_TEMP/gateway.log"`,
		`(cd windows/gateway/launcher && go test -count=1 -v ./...) | tee -a "$RUNNER_TEMP/gateway.log"`,
		`if grep -- '--- SKIP' "$RUNNER_TEMP/gateway.log"; then echo "::error::a test skipped"; exit 1; fi`,
	)
}
