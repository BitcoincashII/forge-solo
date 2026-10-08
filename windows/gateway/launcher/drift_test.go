package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The files this module shares with Forge Solo's launcher are the same bytes, so that a later
// change can move them into one package: a fix made in one and not the other would otherwise go
// unnoticed. session_windows.go differs in its window's class and title alone.
func TestSharedFilesAreForgeSolos(t *testing.T) {
	for _, name := range []string{"clipboard_windows.go", "clipboard_other.go", "log.go", "proc_windows.go",
		"proc_other.go", "session_other.go", "instance_other.go", "go.sum"} {
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
