package forgesolo

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Forge Gateway for Windows is three things built apart that meet only on the user's PC:
// forge-gateway.exe (cmd/forge-gateway), its tray app (windows/gateway/launcher, a Go module of its
// own) and its installer (windows/gateway/forge-gateway.iss). A name one of them spells differently
// compiles and passes its own tests, and then the tray starts nothing, the firewall lets the wrong
// program in, or Setup cannot close the app. Each side's tests pin its own value; these pin that the
// sides agree.

const (
	gwContractTray    = "windows/gateway/launcher/"
	gwContractGateway = "cmd/forge-gateway/"
	gwContractISS     = "windows/gateway/forge-gateway.iss"
)

// gwContractParse is a Go file of the tree, parsed.
func gwContractParse(t *testing.T, file string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("GWC-PARSE: %v", err)
	}
	return f
}

// gwContractLit is a Go literal's value: a string unquoted, a number as written.
func gwContractLit(t *testing.T, lit *ast.BasicLit) string {
	t.Helper()
	if lit.Kind != token.STRING {
		return lit.Value
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		t.Fatalf("GWC-PARSE: %s: %v", lit.Value, err)
	}
	return s
}

// gwContractValues is every top-level constant and variable of the files that is a plain literal,
// by name.
func gwContractValues(t *testing.T, files ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, file := range files {
		for _, d := range gwContractParse(t, file).Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || (g.Tok != token.CONST && g.Tok != token.VAR) {
				continue
			}
			for _, s := range g.Specs {
				v := s.(*ast.ValueSpec)
				for i, n := range v.Names {
					if i < len(v.Values) {
						if lit, ok := v.Values[i].(*ast.BasicLit); ok {
							out[n.Name] = gwContractLit(t, lit)
						}
					}
				}
			}
		}
	}
	return out
}

// gwContractValue is one top-level constant or variable of the files, failing the test without it.
func gwContractValue(t *testing.T, code, name string, files ...string) string {
	t.Helper()
	v, ok := gwContractValues(t, files...)[name]
	if !ok {
		t.Fatalf("%s: %s is not a plain constant in %v", code, name, files)
	}
	return v
}

// gwContractStrings is every string literal inside node.
func gwContractStrings(t *testing.T, node ast.Node) []string {
	t.Helper()
	var out []string
	ast.Inspect(node, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			out = append(out, gwContractLit(t, lit))
		}
		return true
	})
	return out
}

// gwContractFunc is the function name declared in file.
func gwContractFunc(t *testing.T, code, file, name string) *ast.FuncDecl {
	t.Helper()
	for _, d := range gwContractParse(t, file).Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Recv == nil && f.Name.Name == name {
			return f
		}
	}
	t.Fatalf("%s: %s declares no func %s", code, file, name)
	return nil
}

// gwContractJSONNames is the JSON names of a struct's fields.
func gwContractJSONNames(t *testing.T, st *ast.StructType) []string {
	t.Helper()
	var out []string
	for _, f := range st.Fields.List {
		if f.Tag == nil {
			continue
		}
		tag := reflect.StructTag(gwContractLit(t, f.Tag)).Get("json")
		if name, _, _ := strings.Cut(tag, ","); name != "" && name != "-" {
			out = append(out, name)
		}
	}
	return out
}

// gwContractISSValue is the first group of re in the installer script, failing the test without it.
func gwContractISSValue(t *testing.T, code, re string) string {
	t.Helper()
	m := regexp.MustCompile(re).FindStringSubmatch(string(mustRead(t, gwContractISS)))
	if m == nil {
		t.Fatalf("%s: %s has nothing matching %s", code, gwContractISS, re)
	}
	return m[1]
}

// Setup closes a running Forge Gateway, and refuses to go on while another account's runs, by the
// mutex the tray holds while it runs.
func TestGatewayContractMutex(t *testing.T) {
	iss := gwContractISSValue(t, "GWC-MUTEX", `(?m)^\s*RunningMutex = '([^']+)';`)
	tray := gwContractValue(t, "GWC-MUTEX", "runningMutex", gwContractTray+"instance_windows.go")
	if iss != tray {
		t.Errorf("GWC-MUTEX: the installer looks for the mutex %q, the tray holds %q", iss, tray)
	}
}

// The installer starts the tray app the build makes, and its firewall rule lets in the program the
// tray starts.
func TestGatewayContractPrograms(t *testing.T) {
	exe := gwContractISSValue(t, "GWC-EXE", `(?m)^#define MyAppExe "([^"]+)"\r?$`)
	w := loadWorkflow(t, ".github/workflows/test.yml")
	m := regexp.MustCompile(`go build -ldflags "-H=windowsgui -s -w" -o (\S+) \.`).FindStringSubmatch(
		stepRun(t, w, "windows", "Vet, staticcheck, build and test Forge Gateway's tray app"))
	if m == nil || path.Base(m[1]) != exe {
		t.Errorf("GWC-EXE: the installer starts %s; CI builds the tray app as %v", exe, m)
	}
	gw := gwContractValue(t, "GWC-EXE", "gatewayExe", gwContractTray+"main.go")
	rule := gwContractISSValue(t, "GWC-EXE", `FirewallRule\('Forge Gateway Miner \(3333\)', '([^']+)'`)
	if rule != gw {
		t.Errorf("GWC-EXE-FIREWALL: the firewall rule lets in %s; the tray starts %s", rule, gw)
	}
	// CI compiles the installer with a placeholder for each program it installs.
	iscc := stepRun(t, w, "windows", "Forge Gateway's installer script compiles")
	for _, p := range []string{exe, gw} {
		if !strings.Contains(iscc, ": > windows/gateway/bin/"+p+";") {
			t.Errorf("GWC-EXE-ISCC: CI compiles the installer without %s in bin", p)
		}
	}
}

// The installer's data folder (the one the uninstaller offers to delete) is the tray's.
func TestGatewayContractDataFolder(t *testing.T) {
	m := regexp.MustCompile(`dataDir = filepath\.Join\(os\.Getenv\("APPDATA"\), "([^"]+)"\)`).FindSubmatch(
		mustRead(t, gwContractTray+"main.go"))
	if m == nil {
		t.Fatal(`GWC-DATA: the tray sets no dataDir under %APPDATA%`)
	}
	found := regexp.MustCompile(`\{userappdata\}\\([^'\\]+)`).FindAllStringSubmatch(string(mustRead(t, gwContractISS)), -1)
	if len(found) == 0 {
		t.Fatal("GWC-DATA: the installer names no folder in {userappdata}")
	}
	for _, f := range found {
		if f[1] != string(m[1]) {
			t.Errorf("GWC-DATA: the installer names {userappdata}\\%s; the tray's data folder is %%APPDATA%%\\%s", f[1], m[1])
		}
	}
}

// What the tray does on the gateway's exit codes is what the gateway means by them.
func TestGatewayContractExitCodes(t *testing.T) {
	tray := gwContractValues(t, gwContractTray+"supervise.go")
	gw := gwContractValues(t, gwContractGateway+"exit.go")
	for _, p := range [][2]string{{"exitConfigMistake", "exitConfig"}, {"exitPortTaken", "exitPort"}} {
		if tray[p[0]] == "" || tray[p[0]] != gw[p[1]] {
			t.Errorf("GWC-EXIT: the tray's %s is %q, the gateway's %s %q", p[0], tray[p[0]], p[1], gw[p[1]])
		}
	}
	if gw["exitConfig"] == gw["exitPort"] {
		t.Errorf("GWC-EXIT: exitConfig and exitPort are both %s", gw["exitConfig"])
	}
}

// The tray starts the gateway with the config flag the gateway has, the settings password and the
// stop on stdin's end, and the gateway reads both from the environment under those names.
func TestGatewayContractStart(t *testing.T) {
	start := gwContractStrings(t, gwContractFunc(t, "GWC-ENV", gwContractTray+"boot.go", "startGateway"))
	gwMain := gwContractParse(t, gwContractGateway+"main.go")
	// call is pkg.fn("name", ...): the name.
	call := func(e ast.Expr, pkg, fn string) (string, bool) {
		c, ok := e.(*ast.CallExpr)
		if !ok || len(c.Args) == 0 {
			return "", false
		}
		sel, ok := c.Fun.(*ast.SelectorExpr)
		if !ok {
			return "", false
		}
		id, ok := sel.X.(*ast.Ident)
		lit, ok2 := c.Args[0].(*ast.BasicLit)
		if !ok || !ok2 || lit.Kind != token.STRING || id.Name != pkg || sel.Sel.Name != fn {
			return "", false
		}
		return gwContractLit(t, lit), true
	}
	var flags []string            // the string flags the gateway defines: fs.String("config", ...)
	getenv := map[string]string{} // what the gateway reads with os.Getenv, and the value it compares it to
	ast.Inspect(gwMain, func(n ast.Node) bool {
		if b, ok := n.(*ast.BinaryExpr); ok && b.Op == token.EQL {
			if name, ok := call(b.X, "os", "Getenv"); ok {
				if y, ok := b.Y.(*ast.BasicLit); ok {
					getenv[name] = gwContractLit(t, y)
				}
			}
		}
		if e, ok := n.(ast.Expr); ok {
			if name, ok := call(e, "os", "Getenv"); ok {
				if _, seen := getenv[name]; !seen {
					getenv[name] = ""
				}
			}
			if name, ok := call(e, "fs", "String"); ok {
				flags = append(flags, name)
			}
		}
		return true
	})
	has := func(list []string, s string) bool {
		for _, v := range list {
			if v == s {
				return true
			}
		}
		return false
	}
	if !has(flags, "config") || !has(start, "-config") {
		t.Errorf("GWC-ARGS: the tray passes %v; the gateway's string flags are %v", start, flags)
	}
	if _, ok := getenv["SETTINGS_PASSWORD"]; !ok || !has(start, "SETTINGS_PASSWORD=") {
		t.Errorf("GWC-ENV: the tray sets SETTINGS_PASSWORD (%v), the gateway reads %v", has(start, "SETTINGS_PASSWORD="), getenv)
	}
	want, ok := getenv["FORGE_STOP_ON_STDIN_EOF"]
	if !ok || want == "" || !has(start, "FORGE_STOP_ON_STDIN_EOF="+want) {
		t.Errorf("GWC-ENV-STOP: the gateway stops on stdin's end when FORGE_STOP_ON_STDIN_EOF is %q; the tray sets %v", want, start)
	}
}

// The tray reads from /api/status fields the gateway gives, has a tip for every state the gateway
// can be in and for no other, and the mode it looks at is one the gateway gives.
func TestGatewayContractStatus(t *testing.T) {
	var view *ast.StructType
	for _, d := range gwContractParse(t, gwContractGateway+"status.go").Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.TYPE {
			for _, s := range g.Specs {
				if ts := s.(*ast.TypeSpec); ts.Name.Name == "statusView" {
					view, _ = ts.Type.(*ast.StructType)
				}
			}
		}
	}
	if view == nil {
		t.Fatal("GWC-API: cmd/forge-gateway/status.go has no struct statusView")
	}
	gives := map[string]bool{}
	for _, n := range gwContractJSONNames(t, view) {
		gives[n] = true
	}
	var reads []string
	ast.Inspect(gwContractFunc(t, "GWC-API", gwContractTray+"state.go", "askGatewayState"), func(n ast.Node) bool {
		if st, ok := n.(*ast.StructType); ok {
			reads = append(reads, gwContractJSONNames(t, st)...)
		}
		return true
	})
	if len(reads) == 0 {
		t.Error("GWC-API: the tray reads no field of /api/status")
	}
	for _, r := range reads {
		if !gives[r] {
			t.Errorf("GWC-API: the tray reads %q from /api/status, which the gateway does not give", r)
		}
	}

	var states []string // the gateway's state constants
	for _, d := range gwContractParse(t, gwContractGateway+"state.go").Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.CONST {
			for _, s := range g.Specs {
				v := s.(*ast.ValueSpec)
				for i, n := range v.Names {
					if i >= len(v.Values) || !strings.HasPrefix(n.Name, "state") {
						continue
					}
					if lit, ok := v.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						states = append(states, gwContractLit(t, lit))
					}
				}
			}
		}
	}
	var tips []string
	for _, d := range gwContractParse(t, gwContractTray+"state.go").Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, s := range g.Specs {
			v, ok := s.(*ast.ValueSpec)
			if !ok || len(v.Names) != 1 || v.Names[0].Name != "stateTips" || len(v.Values) != 1 {
				continue
			}
			for _, e := range v.Values[0].(*ast.CompositeLit).Elts {
				tips = append(tips, gwContractLit(t, e.(*ast.KeyValueExpr).Key.(*ast.BasicLit)))
			}
		}
	}
	sort.Strings(states)
	sort.Strings(tips)
	if len(states) == 0 || !reflect.DeepEqual(states, tips) {
		t.Errorf("GWC-STATES: the gateway's states are %v; the tray has tips for %v", states, tips)
	}

	// The modes are what the gateway's mode method returns.
	var modeFunc *ast.FuncDecl
	for _, d := range gwContractParse(t, gwContractGateway+"state.go").Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Recv != nil && f.Name.Name == "mode" {
			modeFunc = f
		}
	}
	if modeFunc == nil {
		t.Fatal("GWC-MODE: cmd/forge-gateway/state.go has no mode method")
	}
	modes := map[string]bool{}
	ast.Inspect(modeFunc, func(n ast.Node) bool {
		if r, ok := n.(*ast.ReturnStmt); ok && len(r.Results) == 1 {
			if lit, ok := r.Results[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				modes[gwContractLit(t, lit)] = true
			}
		}
		return true
	})
	var looked []string
	ast.Inspect(gwContractParse(t, gwContractTray+"tips.go"), func(n ast.Node) bool {
		if b, ok := n.(*ast.BinaryExpr); ok && b.Op == token.EQL {
			if id, ok := b.X.(*ast.Ident); ok && id.Name == "mode" {
				if lit, ok := b.Y.(*ast.BasicLit); ok {
					looked = append(looked, gwContractLit(t, lit))
				}
			}
		}
		return true
	})
	if len(looked) == 0 {
		t.Error("GWC-MODE: the tray looks at no mode")
	}
	for _, m := range looked {
		if !modes[m] {
			t.Errorf("GWC-MODE: the tray looks for the mode %q, which the gateway never gives (%v)", m, modes)
		}
	}
}

// The config the tray writes for a new install is the one the gateway's tests read as not set up.
func TestGatewayContractFreshConfig(t *testing.T) {
	tray := gwContractValue(t, "GWC-FRESH", "freshConfig", gwContractTray+"config.go")
	if got := mustRead(t, gwContractGateway+"testdata/fresh-config.json"); string(got) != tray {
		t.Errorf("GWC-FRESH: the tray's freshConfig is not cmd/forge-gateway/testdata/fresh-config.json:\n%s\n--- testdata:\n%s", tray, got)
	}
}

// The tray, the installer and the shortcuts show Forge Solo's BCH2 logo.
func TestGatewayContractIcon(t *testing.T) {
	if !bytes.Equal(mustRead(t, gwContractTray+"forge-gateway.ico"), mustRead(t, "windows/launcher/forge-solo.ico")) {
		t.Error("GWC-ICO: windows/gateway/launcher/forge-gateway.ico is not windows/launcher/forge-solo.ico")
	}
}

// A Forge Gateway Windows service (forge-gateway.exe install) is the one the installer offers to
// remove and the tray names when it holds a port.
func TestGatewayContractService(t *testing.T) {
	gw := gwContractValue(t, "GWC-SERVICE", "serviceName", gwContractGateway+"service_windows.go")
	tray := gwContractValue(t, "GWC-SERVICE", "gatewayService", gwContractTray+"ports_windows.go")
	iss := gwContractISSValue(t, "GWC-SERVICE", `(?m)^\s*ServiceName = '([^']+)';`)
	key := gwContractISSValue(t, "GWC-SERVICE", `(?m)^\s*ServiceKey = '([^']+)';`)
	if gw != tray || gw != iss || key != `SYSTEM\CurrentControlSet\Services\`+gw {
		t.Errorf("GWC-SERVICE: the gateway installs the service %q, the tray looks for %q, the installer for %q (key %q)", gw, tray, iss, key)
	}
}

// Setup says when Forge Solo is on the PC by Forge Solo's own uninstall key and mutex, and the tray
// tells Forge Solo from another program by that mutex.
func TestGatewayContractForgeSolo(t *testing.T) {
	m := regexp.MustCompile(`(?m)^AppId=\{\{([0-9A-F-]+)\}\r?$`).FindSubmatch(mustRead(t, "windows/forge-solo.iss"))
	if m == nil {
		t.Fatal("GWC-SOLO-APPID: windows/forge-solo.iss has no AppId")
	}
	key := gwContractISSValue(t, "GWC-SOLO-APPID", `(?m)^\s*ForgeSoloUninstallKey = '([^']+)';`)
	if key != `Software\Microsoft\Windows\CurrentVersion\Uninstall\{`+string(m[1])+`}_is1` {
		t.Errorf("GWC-SOLO-APPID: the installer looks for Forge Solo at %s; Forge Solo's AppId is %s", key, m[1])
	}
	solo := gwContractValue(t, "GWC-SOLO-MUTEX", "runningMutex", "windows/launcher/instance_windows.go")
	iss := gwContractISSValue(t, "GWC-SOLO-MUTEX", `(?m)^\s*ForgeSoloMutex = '([^']+)';`)
	var tray []string
	for _, s := range gwContractStrings(t, gwContractFunc(t, "GWC-SOLO-MUTEX", gwContractTray+"ports_windows.go", "forgeSoloRunsOS")) {
		if strings.HasPrefix(s, `Global\`) {
			tray = append(tray, s)
		}
	}
	if iss != solo || len(tray) != 1 || tray[0] != solo {
		t.Errorf("GWC-SOLO-MUTEX: Forge Solo holds %q; the installer looks for %q, the tray for %v", solo, iss, tray)
	}
}
