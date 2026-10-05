package stratum

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A miner sending shares below the floor has its difficulty halved every 20 of them, and the line
// saying so was outside every budget: 32 connections, each logged in with a difficulty hint as high
// as the network's, wrote 672 of them in ten seconds, about 4000 a minute, on a port open to the
// internet. They count in the miners' budget now, with the rest of what a miner causes.
func TestSharesBelowTheFloorCannotFillTheLog(t *testing.T) {
	s, rl, logs := observedServer(t, &ServerConfig{MaxConnections: 256, MaxConnectionsPerIP: 128, ExtraNonce1Size: 4,
		ExtraNonce2Size: 8, MinDiff: 1024, MaxDiff: 1e12, VardiffEnabled: true, TargetShareTime: 5, RetargetTime: 10,
		SoloOnly: true, CreditPayoutAddress: true})
	job := soloTestJob("1")
	job.NBits = "1903444b" // about 1.3e9
	s.jobHistory.Store("1", job)
	s.currentJob.Store(job)
	addr := rl.Addr().String()
	// 440 shares below the floor halve the hinted difficulty down to the floor, 1024: 21 lines.
	const conns, each = 32, 440
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < conns; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := net.Dial("tcp", addr)
			if err != nil {
				return
			}
			defer c.Close()
			var b strings.Builder
			fmt.Fprintf(&b, `{"id":1,"method":"mining.subscribe","params":["x"]}`+"\n")
			fmt.Fprintf(&b, `{"id":2,"method":"mining.authorize","params":["%s.w%d","d=1000000000000"]}`+"\n", testPayout, i)
			for n := 1; n <= each; n++ {
				fmt.Fprintf(&b, `{"id":%d,"method":"mining.submit","params":["%s.w%d","1","%016x","%s","%08x"]}`+"\n",
					10+n, testPayout, i, n, job.NTime, n)
			}
			c.Write([]byte(b.String()))
			c.SetReadDeadline(time.Now().Add(30 * time.Second))
			r, last := bufio.NewReader(c), fmt.Sprintf(`"id":%d,`, 10+each)
			for {
				line, err := r.ReadString('\n')
				if err != nil || strings.Contains(line, last) {
					return
				}
			}
		}(i)
	}
	wg.Wait()
	settled(t, s, rl, conns, 0)
	elapsed := time.Since(start)
	const msg = "High rejection rate, reducing difficulty"
	halved := logs.FilterMessage(msg).Len()
	if halved == 0 {
		t.Fatal("LOGB-REJECT-SETUP: no difficulty was halved")
	}
	if limit := serverLogBudget + minerLogBurst + int(elapsed.Minutes()*(serverLogBudget+minerLogRate)) + 1; logs.Len() > limit {
		t.Errorf("LOGB-REJECT-FLOOD: %d connections sending shares below the floor wrote %d lines in %v, %d of them halving a difficulty; the budgets allow %d",
			conns, logs.Len(), elapsed.Round(time.Millisecond), halved, limit)
	}
	s.Stop()
	if by, _ := summedUp(logs, overMiners); by[msg] != int64(conns*21-halved) {
		t.Errorf("LOGB-REJECT-COUNTED: %d lines halving a difficulty were left out, summed up as %d", conns*21-halved, by[msg])
	}
}

// linesOutsideTheBudgets are the lines the stratum writes outside the log budgets, by how they
// start, and why a client cannot write them at will.
var linesOutsideTheBudgets = map[string]string{
	"Stratum server started":                                             "the stratum starting",
	"Initiating graceful shutdown...":                                    "the stratum stopping",
	"Closing the miners' connections":                                    "the stratum stopping",
	"Shares still being processed at shutdown":                           "the stratum stopping",
	"Graceful shutdown complete":                                         "the stratum stopping",
	"Disconnected all clients":                                           "the gateway closing its door",
	"Log lines left out to keep the log from filling":                    "one for each budget a cleanup round",
	"Marketplace health check logged in and closed again: the next ones": "the first health check only",
	"Marketplace health checks logged in and closed again":               "one every 10 minutes",
	"submit exceeded the intake rate limit but SOLVES A BLOCK":           "a block",
	"A refused share solves a 1175 block; sending it to the 1175 node":   "a 1175 block, each sent once",
	"aux: assemble failed":                                               "a 1175 block",
	"aux: block rejected (likely stale aux tip)":                         "a 1175 block",
	"aux: submitauxblock error":                                          "a 1175 block",
	"aux: gave up submitting a solved 1175 block":                        "a 1175 block",
	"aux: submitauxblock got no answer; trying again":                    "a 1175 block",
	"🎉 AUX (1175) BLOCK FOUND":                                           "a 1175 block",
	"in limitedLog":                                                      "the budgets' own writer",
}

// Every line a client can cause goes through a log budget. Lines about a miner's difficulty were
// written outside them, and a client could have one written for every 20 shares it sent: no line
// the stratum writes at info or above is outside the budgets, but those listed in
// linesOutsideTheBudgets.
func TestEveryLineAClientCausesIsBudgeted(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	used := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !writesALine(call) {
					return true
				}
				msg := "in " + fd.Name.Name
				if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					msg, _ = strconv.Unquote(lit.Value)
				}
				for start := range linesOutsideTheBudgets {
					if strings.HasPrefix(msg, start) {
						used[start] = true
						return true
					}
				}
				t.Errorf("LOGB-UNBUDGETED: %s writes %q outside the log budgets: write it with clientLog, minerLog or limitedLog",
					fset.Position(call.Pos()), msg)
				return true
			})
		}
	}
	for start := range linesOutsideTheBudgets {
		if !used[start] {
			t.Errorf("LOGB-UNBUDGETED-LIST: no line starts %q any more: take it off the list", start)
		}
	}
}

// writesALine reports whether call is <x>.logger.Info, Warn, Error or worse, with a message.
func writesALine(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || len(call.Args) == 0 {
		return false
	}
	switch sel.Sel.Name {
	case "Info", "Warn", "Error", "DPanic", "Panic", "Fatal":
	default:
		return false
	}
	switch x := sel.X.(type) {
	case *ast.SelectorExpr:
		return x.Sel.Name == "logger"
	case *ast.Ident:
		return x.Name == "logger"
	}
	return false
}
