package main

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Windows 11 shows the first 64 characters of a tray icon's tooltip: a text past 63 lost its end
// (the move's messages were cut mid-sentence), and what came first was all anyone read.
const shownChars = 63

// launcherCode is the launcher's own code, parsed: every file but the tests, whatever its platform.
func launcherCode(t *testing.T) (*token.FileSet, []*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	return fset, files
}

// constText is the value of a constant text in the launcher's code: literals, constants and their
// sums.
func constText(e ast.Expr, consts map[string]ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil
	case *ast.Ident:
		if v, ok := consts[e.Name]; ok {
			return constText(v, consts)
		}
	case *ast.ParenExpr:
		return constText(e.X, consts)
	case *ast.BinaryExpr:
		if e.Op == token.ADD {
			a, ok1 := constText(e.X, consts)
			b, ok2 := constText(e.Y, consts)
			return a + b, ok1 && ok2
		}
	}
	return "", false
}

// startWhys are the tray's reasons for a start that failed, from the errors the code gives: each
// required public port held by another program or kept by Windows, secrets.env that cannot be read
// or written, and no free local port for each of the services.
func startWhys(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, p := range publicPorts {
		if p.required {
			for _, reserved := range []bool{false, true} {
				out = append(out, startWhy(&portError{p.port, p.what, errors.New("held"), reserved}))
			}
		}
	}
	savedData, savedSec, savedPlan := dataDir, sec, portPlan
	t.Cleanup(func() { dataDir, sec, portPlan = savedData, savedSec, savedPlan })
	for _, folder := range []string{"secrets.env", "secrets.env.tmp"} { // read, then written
		dataDir, sec = t.TempDir(), secrets{}
		if err := os.Mkdir(dpath(folder), 0o755); err != nil {
			t.Fatal(err)
		}
		err := setupSecrets()
		if err == nil {
			t.Fatalf("setup: secrets.env was read and written with %s a folder", folder)
		}
		out = append(out, startWhy(err))
	}
	for _, p := range savedPlan {
		port := ""
		portPlan = []portSlot{{p.name, &port, 70000}} // past the last port there is
		err := assignPorts()
		if err == nil {
			t.Fatalf("setup: a port was found for %s past the last one", p.name)
		}
		out = append(out, startWhy(err))
	}
	return out
}

// Every text the tray can show fits in what Windows 11 shows, and the tray shows no other. The
// texts are tips.go's: each constant, and each function with every value the code gives it. Every
// place that sets the tooltip, or a trouble or a note it shows, must take one of them.
func TestEveryTrayTextFits(t *testing.T) {
	fset, files := launcherCode(t)
	consts := map[string]ast.Expr{}
	for _, f := range files {
		for _, d := range f.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.CONST {
				for _, s := range g.Specs {
					v := s.(*ast.ValueSpec)
					for i, n := range v.Names {
						if i < len(v.Values) {
							consts[n.Name] = v.Values[i]
						}
					}
				}
			}
		}
	}
	measure := func(from, s string) {
		n := len([]rune(s))
		t.Logf("%-22s %2d %s", from, n, s)
		if n > shownChars {
			t.Errorf("TIP-TOO-LONG(%s): %d characters, past the %d Windows 11 shows: %q", from, n, shownChars, s)
		}
	}

	// tips.go's constants and functions.
	tipConsts, tipFuncs := map[string]bool{}, map[string]bool{}
	var notes []string
	for _, f := range files {
		if fset.Position(f.Pos()).Filename != "tips.go" {
			continue
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				if d.Tok != token.CONST {
					continue
				}
				for _, s := range d.Specs {
					for _, n := range s.(*ast.ValueSpec).Names {
						if n.Name == "tipMax" {
							continue
						}
						s, ok := constText(n, consts)
						switch {
						case !ok:
							t.Errorf("TIP-CONST: %s is not a constant text", n.Name)
						case strings.HasPrefix(n.Name, "tip"):
							tipConsts[n.Name] = true
							measure(n.Name, s)
						case strings.HasPrefix(n.Name, "note"):
							tipConsts[n.Name] = true
							notes = append(notes, s)
						}
					}
				}
			case *ast.FuncDecl:
				if strings.HasPrefix(d.Name.Name, "tip") {
					tipFuncs[d.Name.Name] = true
				}
			}
		}
	}
	if len(tipConsts) == 0 || len(tipFuncs) == 0 || len(notes) == 0 {
		t.Fatalf("setup: tips.go has %d texts, %d notes and %d functions", len(tipConsts), len(notes), len(tipFuncs))
	}
	var programs, nodeNames []string
	for _, k := range runningKeys {
		_, what := supervisedProgram(k)
		programs = append(programs, what)
	}
	for _, n := range nodes() {
		_, what := supervisedProgram(n.key)
		nodeNames = append(nodeNames, what)
	}
	each := func(values []string, f func(string) string) (out []string) {
		for _, v := range values {
			out = append(out, f(v))
		}
		return out
	}
	made := map[string]func() []string{
		"tipRunningWith": func() []string { return each(append([]string{""}, notes...), tipRunningWith) },
		"tipCannotStart": func() []string { return each(startWhys(t), tipCannotStart) },
		"tipNoDashboard": func() []string { return []string{tipNoDashboard(webPort)} },
		// What Windows says when an antivirus holds a program; a longer reason is cut short.
		"tipProgramCannotStart": func() []string {
			return each(programs, func(what string) string { return tipProgramCannotStart(what, "Access is denied.") })
		},
		"tipRestarting":   func() []string { return each(programs, tipRestarting) },
		"tipRebuilding":   func() []string { return each(nodeNames, tipRebuilding) },
		"tipChainDamaged": func() []string { return each(nodeNames, tipChainDamaged) },
	}
	for name := range tipFuncs {
		texts, ok := made[name]
		if !ok {
			t.Errorf("TIP-NO-VALUES: tips.go's %s makes texts this test does not measure", name)
			continue
		}
		for _, s := range texts() {
			measure(name, s)
		}
	}

	// Every place that sets the tooltip, a trouble or a note takes one of tips.go's texts. The
	// functions that pass one on (status, and what calls it with the text it was given) are where
	// that text is checked.
	passOn := map[string]bool{"status": true, "showTrouble": true, "setTrouble": true, "startFailed": true}
	isTip := func(e ast.Expr) bool {
		switch e := e.(type) {
		case *ast.Ident:
			return tipConsts[e.Name] && strings.HasPrefix(e.Name, "tip")
		case *ast.CallExpr:
			id, ok := e.Fun.(*ast.Ident)
			return ok && tipFuncs[id.Name]
		}
		return false
	}
	sites := 0
	for _, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := ""
				switch c := call.Fun.(type) {
				case *ast.Ident:
					name = c.Name
				case *ast.SelectorExpr:
					if x, ok := c.X.(*ast.Ident); ok && x.Name == "systray" && c.Sel.Name == "SetTooltip" {
						name = "systray.SetTooltip"
					}
				}
				at := fset.Position(call.Pos())
				var arg ast.Expr
				switch name {
				case "status", "startFailed", "setTooltip", "systray.SetTooltip":
					arg = call.Args[0]
				case "setTrouble":
					arg = call.Args[1]
				case "setRunningNote":
					sites++
					a := call.Args[0]
					id, isID := a.(*ast.Ident)
					lit, isLit := a.(*ast.BasicLit)
					if !(isID && tipConsts[id.Name] && strings.HasPrefix(id.Name, "note")) && !(isLit && lit.Value == `""`) {
						t.Errorf("TIP-NOTE-SITE: %s:%d sets a note that is not one of tips.go's", at.Filename, at.Line)
					}
					return true
				default:
					return true
				}
				sites++
				passed := false
				if passOn[fn.Name.Name] {
					switch a := arg.(type) {
					case *ast.Ident:
						passed = true
					case *ast.CallExpr:
						if id, ok := a.Fun.(*ast.Ident); ok && id.Name == "trimTip" && len(a.Args) == 1 {
							_, passed = a.Args[0].(*ast.Ident)
						}
					}
				}
				if !passed && !isTip(arg) {
					t.Errorf("TIP-CALL-SITE: %s:%d (%s) shows a text that is not one of tips.go's", at.Filename, at.Line, name)
				}
				return true
			})
		}
	}
	if sites < 20 {
		t.Fatalf("setup: only %d places that set the tray's texts were found", sites)
	}
}

// A text the tray shows is cut to 63 characters, the last an ellipsis, whatever is passed: a
// program's own reason for not starting can be long.
func TestTheTrayCutsALongText(t *testing.T) {
	saved := setTooltip
	var got string
	setTooltip = func(s string) { got = s }
	tipMu.Lock()
	shown := stopShown
	stopShown = false
	tipMu.Unlock()
	t.Cleanup(func() {
		tipMu.Lock()
		stopShown = shown
		tipMu.Unlock()
		setTooltip = saved
	})
	long := tipProgramCannotStart("the dashboard's API", "The system cannot find the file specified.")
	status(long)
	if r := []rune(got); len(r) != shownChars || !strings.HasSuffix(got, "…") || !strings.HasPrefix(long, string(r[:shownChars-1])) {
		t.Errorf("TIP-CUT: %q was shown as %q (%d characters)", long, got, len(r))
	}
	status(tipRunning)
	if got != tipRunning {
		t.Errorf("TIP-CUT-SHORT: %q was shown as %q", tipRunning, got)
	}
}
