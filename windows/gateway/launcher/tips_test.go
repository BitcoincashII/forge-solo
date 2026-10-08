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

// Windows 11 shows the first 64 characters of a tray icon's tooltip: a text past 63 loses its end,
// and what comes first is all anyone reads.
const shownChars = 63

// trayCode is the tray app's own code, parsed: every file but the tests, whatever its platform.
func trayCode(t *testing.T) (*token.FileSet, []*ast.File) {
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

// constText is the value of a constant text in the tray's code: literals, constants and their sums.
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
// port held by each kind of holder or kept by Windows, at its longest (65535), secrets.env that
// cannot be read or written, and a config that cannot be written.
func startWhys(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, what := range []string{"the miner port", "the status page"} {
		for _, h := range []string{holderSolo, holderGateway, holderService, holderOther} {
			for _, reserved := range []bool{false, true} {
				out = append(out, startWhy(&portError{"65535", what, errors.New("held"), reserved, h}))
			}
		}
	}
	savedData, savedSec := dataDir, sec
	t.Cleanup(func() { dataDir, sec = savedData, savedSec })
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
	dataDir = filepath.Join(t.TempDir(), "gone")
	err := ensureConfig()
	if err == nil {
		t.Fatal("setup: forge-gateway.json was written in a folder that is not there")
	}
	return append(out, startWhy(err))
}

// Every text the tray can show fits in what Windows 11 shows, and the tray shows no other. The
// texts are tips.go's: each constant, and each function with every value the code gives it. Every
// place that sets the tooltip, or a trouble it shows, must take one of them.
func TestEveryTrayTextFits(t *testing.T) {
	fset, files := trayCode(t)
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
			t.Errorf("GWL-TIPS(%s): %d characters, past the %d Windows 11 shows: %q", from, n, shownChars, s)
		}
	}

	// tips.go's constants and functions.
	tipConsts, tipFuncs := map[string]bool{}, map[string]bool{}
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
							t.Errorf("GWL-TIPS-CONST: %s is not a constant text", n.Name)
						case strings.HasPrefix(n.Name, "tip"):
							tipConsts[n.Name] = true
							measure(n.Name, s)
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
	if len(tipConsts) < 10 || len(tipFuncs) < 4 {
		t.Fatalf("setup: tips.go has %d texts and %d functions", len(tipConsts), len(tipFuncs))
	}
	_, gateway := supervisedProgram(gatewayKey)
	var states []string
	for s := range stateTips {
		states = append(states, s)
	}
	made := map[string]func() []string{
		"tipCannotStart": func() []string {
			var out []string
			for _, why := range startWhys(t) {
				out = append(out, tipCannotStart(why))
			}
			return out
		},
		// What Windows says when an antivirus holds a program; a longer reason is cut short.
		"tipProgramCannotStart": func() []string { return []string{tipProgramCannotStart(gateway, "Access is denied.")} },
		"tipRestarting":         func() []string { return []string{tipRestarting(gateway)} },
		"tipForState": func() []string {
			var out []string
			for _, s := range append(states, "a_state_to_come") {
				for _, m := range []string{"tides", "solo", "waiting", "starting", "off", ""} {
					out = append(out, tipForState(s, m))
				}
			}
			return out
		},
	}
	for name := range tipFuncs {
		texts, ok := made[name]
		if !ok {
			t.Errorf("GWL-TIPS-NO-VALUES: tips.go's %s makes texts this test does not measure", name)
			continue
		}
		for _, s := range texts() {
			measure(name, s)
		}
	}

	// Every place that sets the tooltip or a trouble takes one of tips.go's texts. The functions
	// that pass one on (status, and what calls it with the text it was given) are where that text is
	// checked.
	passOn := map[string]bool{"status": true, "showTrouble": true, "setTrouble": true, "startFailed": true}
	isTip := func(e ast.Expr) bool {
		switch e := e.(type) {
		case *ast.Ident:
			return tipConsts[e.Name]
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
					t.Errorf("GWL-TIPS-CALL-SITE: %s:%d (%s) shows a text that is not one of tips.go's", at.Filename, at.Line, name)
				}
				return true
			})
		}
	}
	if sites < 15 {
		t.Fatalf("setup: only %d places that set the tray's texts were found", sites)
	}
}

// A text the tray shows is cut to 63 characters, the last an ellipsis, whatever is passed: a
// program's own reason for not starting can be long.
func TestTheTrayCutsALongText(t *testing.T) {
	w := gatewayWorld(t, "eof")
	long := tipProgramCannotStart(gatewayExe, "The system cannot find the file specified.")
	status(long)
	got := w.tips.last()
	if r := []rune(got); len(r) != shownChars || !strings.HasSuffix(got, "…") || !strings.HasPrefix(long, string(r[:shownChars-1])) {
		t.Errorf("GWL-TIPS-CUT: %q was shown as %q (%d characters)", long, got, len(r))
	}
	status(tipActive)
	if w.tips.last() != tipActive {
		t.Errorf("GWL-TIPS-CUT-SHORT: %q was shown as %q", tipActive, w.tips.last())
	}
}

// Every state the gateway gives has its tip, and the tips are the tray's, each said once: a state
// two tips share would hide which it is.
func TestEveryStateHasItsTip(t *testing.T) {
	want := map[string]string{"unconfigured": tipSetUp, "node_unreachable": tipNodeUnreachable, "node_login": tipNodeLogin,
		"node_forbidden": tipNodeForbidden, "node_syncing": tipNodeSyncing, "pool_unreachable": tipPoolSolo, "clock_off": tipClockSolo,
		"starting": tipWaitingForWork, "active": tipActive}
	if len(stateTips) != len(want) {
		t.Errorf("GWL-STATE-TABLE: stateTips has %d states, want the gateway's %d", len(stateTips), len(want))
	}
	for s, tip := range want {
		if stateTips[s] != tip {
			t.Errorf("GWL-STATE-TABLE: the gateway's %s is said as %q, want %q", s, stateTips[s], tip)
		}
	}
	if tipForState("pool_unreachable", "waiting") != tipPoolWaiting || tipForState("pool_unreachable", "solo") != tipPoolSolo {
		t.Error("GWL-STATE-TABLE: a pool out of reach is not said as the gateway's mode has it")
	}
	if tipForState("clock_off", "waiting") != tipClockWaiting || tipForState("clock_off", "solo") != tipClockSolo {
		t.Error("GWL-STATE-CLOCK: a clock the pool refuses is not said as the gateway's mode has it")
	}
	// A node that refuses this PC is not a wrong login: the tray does not send the user to retype
	// the password in Settings.
	if tipNodeForbidden == tipNodeLogin || strings.Contains(tipNodeForbidden, "log in") || !strings.Contains(tipNodeForbidden, "refuses this PC") {
		t.Errorf("GWL-STATE-FORBIDDEN: a node that refuses this PC is said as %q", tipNodeForbidden)
	}
	// The tray's texts name no key of the config file: its user never sees that file.
	for s, tip := range stateTips {
		if strings.Contains(tip, "rpc_") || strings.Contains(tip, "node.") {
			t.Errorf("GWL-STATE-PLAIN: %s is said as %q", s, tip)
		}
	}
}
