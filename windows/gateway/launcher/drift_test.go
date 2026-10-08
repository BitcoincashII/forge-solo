package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The files this module shares with Forge Solo's launcher are the same bytes, so that a later
// change can move them into one package: a fix made in one and not the other would otherwise go
// unnoticed. session_windows.go differs in its window's class and title alone.
func TestSharedFilesAreForgeSolos(t *testing.T) {
	for _, name := range []string{"clipboard_windows.go", "clipboard_other.go", "log.go", "proc_windows.go",
		"proc_other.go", "session_other.go", "instance_other.go"} {
		ours, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		theirs, err := os.ReadFile(filepath.Join("..", "..", "launcher", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(ours, theirs) {
			t.Errorf("GWL-SHARED-DRIFT: %s is not windows/launcher/%s", name, name)
		}
	}
	ours, err := os.ReadFile("session_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := os.ReadFile(filepath.Join("..", "..", "launcher", "session_windows.go"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(string(theirs), `UTF16PtrFromString("ForgeSoloSession")`, `UTF16PtrFromString("ForgeGatewaySession")`, 1)
	want = strings.Replace(want, `UTF16PtrFromString("Forge Solo")`, `UTF16PtrFromString("Forge Gateway")`, 1)
	if want == string(theirs) || string(ours) != want {
		t.Error("GWL-SHARED-DRIFT: session_windows.go is not windows/launcher/session_windows.go with its window's class and title changed")
	}
}

// requires is what a go.mod requires: each module's version, by path.
func requires(gomod []byte) map[string]string {
	out := map[string]string{}
	block := false
	for _, line := range strings.Split(string(gomod), "\n") {
		line, _, _ = strings.Cut(line, "//")
		f := strings.Fields(line)
		switch {
		case len(f) == 0:
		case block && f[0] == ")":
			block = false
		case f[0] == "require" && len(f) == 2 && f[1] == "(":
			block = true
		case f[0] == "require" && len(f) == 3:
			out[f[1]] = f[2]
		case block && len(f) == 2:
			out[f[0]] = f[1]
		}
	}
	return out
}

// sharedVersionsDiffer is, for each module both go.mod files require, a line saying how their
// versions differ; nothing when they are the same.
func sharedVersionsDiffer(ours, theirs []byte) []string {
	a, b := requires(ours), requires(theirs)
	var out []string
	for path, v := range a {
		if w, ok := b[path]; ok && w != v {
			out = append(out, path+": "+v+" in windows/gateway/launcher, "+w+" in windows/launcher")
		}
	}
	sort.Strings(out)
	return out
}

// The two launchers build the files they share against the same modules: each module both require
// is at the same version in both. go.sum is not compared byte for byte, so a module one of them
// alone needs does not fail this. A Dependabot pull request for one launcher alone (a security
// update is made per folder) fails this until the other launcher has the same version.
func TestSharedModulesAreAtTheSameVersions(t *testing.T) {
	ours, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := os.ReadFile(filepath.Join("..", "..", "launcher", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if len(requires(ours)) < 3 || len(requires(theirs)) < 3 {
		t.Fatalf("setup: go.mod requires %v here and %v in windows/launcher", requires(ours), requires(theirs))
	}
	for _, d := range sharedVersionsDiffer(ours, theirs) {
		t.Errorf("GWL-SHARED-VERSIONS: %s. The two Windows launchers share code and must build with the same versions: "+
			"update the other launcher too (in its folder, go get <module>@<the newer version>, then go mod tidy), "+
			"in the same pull request", d)
	}
}

// What the comparison takes for the same versions and for different ones.
func TestSharedVersionsCompare(t *testing.T) {
	gomod := func(sys string) []byte {
		return []byte("module m\n\ngo 1.26.0\n\nrequire (\n\tfyne.io/systray v1.12.2\n\tgolang.org/x/sys " + sys +
			" // a comment\n)\n\nrequire github.com/godbus/dbus/v5 v5.1.0 // indirect\n")
	}
	if d := sharedVersionsDiffer(gomod("v0.48.0"), append(gomod("v0.48.0"), "require example.com/only/here v1.0.0\n"...)); len(d) != 0 {
		t.Errorf("GWL-SHARED-VERSIONS-SAME: the same versions were taken for different ones: %v", d)
	}
	d := sharedVersionsDiffer(gomod("v0.49.0"), gomod("v0.48.0"))
	if len(d) != 1 || d[0] != "golang.org/x/sys: v0.49.0 in windows/gateway/launcher, v0.48.0 in windows/launcher" {
		t.Errorf("GWL-SHARED-VERSIONS-DIFFER: a newer golang.org/x/sys in one launcher gives %v", d)
	}
	if got := requires(gomod("v0.48.0")); len(got) != 3 || got["github.com/godbus/dbus/v5"] != "v5.1.0" {
		t.Errorf("GWL-SHARED-VERSIONS-READ: %v", got)
	}
}

// stringConsts parses a file of this module, whatever its platform, and gives its constants that
// are string literals, and every string literal in it.
func stringConsts(t *testing.T, name string) (map[string]string, []string) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	consts := map[string]string{}
	var lits []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.GenDecl:
			if n.Tok != token.CONST {
				return true
			}
			for _, s := range n.Specs {
				v := s.(*ast.ValueSpec)
				for i, id := range v.Names {
					if i < len(v.Values) {
						if l, ok := v.Values[i].(*ast.BasicLit); ok && l.Kind == token.STRING {
							consts[id.Name], _ = strconv.Unquote(l.Value)
						}
					}
				}
			}
		case *ast.BasicLit:
			if n.Kind == token.STRING {
				s, _ := strconv.Unquote(n.Value)
				lits = append(lits, s)
			}
		}
		return true
	})
	return consts, lits
}

// One Forge Gateway runs on a PC, whatever the account: the mutex is global, and it is the one the
// installer looks for, not Forge Solo's.
func TestTheMutexIsForgeGateways(t *testing.T) {
	consts, _ := stringConsts(t, "instance_windows.go")
	if got := consts["runningMutex"]; got != `Global\ForgeGatewayRunning` {
		t.Errorf("GWL-MUTEX: runningMutex is %q, not Global\\ForgeGatewayRunning", got)
	}
}

// The hidden window that hears Windows end the session is Forge Gateway's: one named Forge Solo in
// Forge Gateway's process would be found by anything that looks for Forge Solo's.
func TestTheSessionWindowIsForgeGateways(t *testing.T) {
	_, lits := stringConsts(t, "session_windows.go")
	found := map[string]bool{}
	for _, s := range lits {
		found[s] = true
		if strings.Contains(s, "ForgeSolo") || s == "Forge Solo" {
			t.Errorf("GWL-SESSION-WINDOW: session_windows.go still has %q", s)
		}
	}
	if !found["ForgeGatewaySession"] || !found["Forge Gateway"] {
		t.Error("GWL-SESSION-WINDOW: the session window's class is not ForgeGatewaySession, or its title not Forge Gateway")
	}
}

// The service the tray names in launcher.log is the one it looks for, and the one forge-gateway.exe
// install makes.
func TestTheServiceIsNamedAsItIsFound(t *testing.T) {
	consts, _ := stringConsts(t, "ports_windows.go")
	name := consts["gatewayService"]
	if name != "ForgeGateway" {
		t.Fatalf("GWL-SERVICE-NAME: gatewayService is %q, not ForgeGateway", name)
	}
	e := &portError{"3333", "the miner port", os.ErrExist, false, holderService}
	for _, s := range []string{e.why(), e.advice(), e.byItself()} {
		if !strings.Contains(s, name) {
			t.Errorf("GWL-SERVICE-NAME: %q does not name the %s service", s, name)
		}
	}
}
