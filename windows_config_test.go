package forgesolo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The Windows launcher writes the stratum's config itself (configYAML in windows/launcher). It must
// stay the config the Umbrel app ships and tests, docker/stratum/config.template.yaml, value for
// value: the stratum ignores keys it does not read, and a value that drifts still looks configured.
// While the launcher had a repository of its own only the keys could be compared, and Windows wrote
// "Forge Solo" as the coinbase tag while Settings showed "Forge".
//
// The launcher is a module of its own, so this reads its source: configYAML's return expression,
// evaluated with stand-ins for what the launcher picks at run time.
func TestWindowsConfigMatchesShippedTemplate(t *testing.T) {
	runtime := map[string]string{ // picked on every launch: free ports and a generated secret
		"bch2RPC": "30301", "bch2ZMQ": "30601", "aux1175RPC": "30901", "sec.AuxPass": "pw-test",
	}
	fset := token.NewFileSet()
	paths, err := filepath.Glob("windows/launcher/*.go")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no launcher sources: %v", err)
	}
	literals := map[string]string{} // package-level names declared with a string literal
	var config ast.Expr
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				for _, s := range d.Specs {
					vs, ok := s.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, n := range vs.Names {
						if i < len(vs.Values) {
							if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
								literals[n.Name], _ = strconv.Unquote(lit.Value)
							}
						}
					}
				}
			case *ast.FuncDecl:
				if d.Name.Name == "configYAML" && d.Recv == nil {
					for _, st := range d.Body.List {
						if r, ok := st.(*ast.ReturnStmt); ok && len(r.Results) == 1 {
							config = r.Results[0]
						}
					}
				}
			}
		}
	}
	if config == nil {
		t.Fatal("windows/launcher has no func configYAML() returning one string")
	}
	var eval func(ast.Expr) string
	eval = func(e ast.Expr) string {
		switch e := e.(type) {
		case *ast.BasicLit:
			s, err := strconv.Unquote(e.Value)
			if err != nil {
				t.Fatalf("configYAML: %v", err)
			}
			return s
		case *ast.ParenExpr:
			return eval(e.X)
		case *ast.BinaryExpr:
			if e.Op != token.ADD {
				t.Fatalf("configYAML: unexpected operator %s", e.Op)
			}
			return eval(e.X) + eval(e.Y)
		case *ast.Ident:
			if v, ok := runtime[e.Name]; ok {
				return v
			}
			if v, ok := literals[e.Name]; ok {
				return v
			}
			t.Fatalf("configYAML uses %s, which is not a string constant; give it a stand-in in this test", e.Name)
		case *ast.SelectorExpr:
			if x, ok := e.X.(*ast.Ident); ok {
				if v, ok := runtime[x.Name+"."+e.Sel.Name]; ok {
					return v
				}
			}
			t.Fatalf("configYAML uses %T at %s; give it a stand-in in this test", e, fset.Position(e.Pos()))
		default:
			t.Fatalf("configYAML: cannot evaluate %T at %s", e, fset.Position(e.Pos()))
		}
		return ""
	}

	var got, want map[string]interface{}
	if err := yaml.Unmarshal([]byte(eval(config)), &got); err != nil {
		t.Fatalf("the Windows config is not YAML: %v", err)
	}
	tmpl, err := os.ReadFile("docker/stratum/config.template.yaml")
	if err != nil {
		t.Fatal(err)
	}
	filled := strings.NewReplacer(
		"${POOL_ADDRESS}", "", "${COINBASE_TAG}", "", "${NODE_HOST}", "127.0.0.1", "${NODE_PORT}", "30301",
		"${ZMQ_ENDPOINT}", "tcp://127.0.0.1:30601", "${PAYOUT_ADDRESS_1175}", "", "${AUX1175_HOST}", "127.0.0.1",
		"${AUX1175_PORT}", "30901", "${AUX1175_USER}", "forge1175", "${AUX1175_PASSWORD}", "pw-test").Replace(string(tmpl))
	if err := yaml.Unmarshal([]byte(filled), &want); err != nil {
		t.Fatalf("template: %v", err)
	}
	for k := range want {
		if !reflect.DeepEqual(got[k], want[k]) {
			t.Errorf("%s:\n got  %v\n want %v", k, got[k], want[k])
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("extra key %s", k)
		}
	}
}
