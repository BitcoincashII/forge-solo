package stratum

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// minerLineServer is a solo stratum with job "a" and a miner on it, whose log a test reads.
func minerLineServer(t *testing.T) (*Server, *Client, *observer.ObservedLogs) {
	t.Helper()
	s, _ := perJobServer()
	core, logs := observer.New(zapcore.InfoLevel)
	s.logger = zap.New(core)
	s.SetSoloPayoutAddress(testPayout)
	job := soloTestJob("a")
	s.jobHistory.Store("a", job)
	s.currentJob.Store(job)
	c := perJobClient(t)
	c.Difficulty = 1e-6
	return s, c, logs
}

// submitting sends c's mining.submit with params, as its connection would.
func submitting(s *Server, c *Client, params ...any) {
	p, _ := json.Marshal(params)
	s.handleMessage(c, []byte(`{"id":9,"method":"mining.submit","params":`+string(p)+`}`))
}

// loggingIn sends mining.authorize as user from c, a connection that has not logged in yet.
func loggingIn(s *Server, c *Client, user string) {
	c.Authorized, c.MinerID, c.WorkerName = false, "", ""
	p, _ := json.Marshal([]string{user, "x"})
	s.handleMessage(c, []byte(`{"id":2,"method":"mining.authorize","params":`+string(p)+`}`))
}

// minerLine is a line the miners' budget holds, and what makes the stratum write it once.
type minerLine struct {
	code, msg string
	cause     func(t *testing.T, s *Server, c *Client)
}

// minerLines is every line about a miner the miners' budget holds, but its disconnect
// (TestARentalStartIsLoggedInFull).
var minerLines = []minerLine{
	{"PARSE", "Failed to parse submit params", func(t *testing.T, s *Server, c *Client) {
		s.handleMessage(c, []byte(`{"id":9,"method":"mining.submit","params":{"job":"a"}}`))
	}},
	{"FEW", "Insufficient submit params", func(t *testing.T, s *Server, c *Client) {
		submitting(s, c, c.WorkerName, "a")
	}},
	{"RANGE", "Invalid numeric parameter - out of range", func(t *testing.T, s *Server, c *Client) {
		submitting(s, c, c.WorkerName, "a", "0000000000000001", -1, "00000000")
	}},
	{"MALFORMED", "Malformed share refused", func(t *testing.T, s *Server, c *Client) {
		submitting(s, c, c.WorkerName, "zz", "0000000000000001", soloTestJob("a").NTime, "00000000")
	}},
	{"NOTFOUND", "Job not found", func(t *testing.T, s *Server, c *Client) {
		submitting(s, c, c.WorkerName, "ee", "0000000000000001", soloTestJob("a").NTime, "00000000")
	}},
	// The one every rental has at each new block.
	{"STALE", "Share on a job from before the last block refused as stale (expected now and then, just after a new block)",
		func(t *testing.T, s *Server, c *Client) {
			next := soloTestJob("b")
			next.PrevBlockHash = strings.Repeat("11", 32)
			s.jobHistory.Store("b", next)
			s.currentJob.Store(next)
			submitting(s, c, c.WorkerName, "a", "0000000000000001", soloTestJob("a").NTime, "00000000")
		}},
	{"NTIME", "Share refused: its ntime is outside its job's time range", func(t *testing.T, s *Server, c *Client) {
		submitting(s, c, c.WorkerName, "a", "0000000000000001", ntimePlus(soloTestJob("a").NTime, 7001), "00000000")
	}},
	{"INVALID", "Share validation error", func(t *testing.T, s *Server, c *Client) {
		c.ExtraNonce1 = "" // logged in without subscribing
		submitting(s, c, c.WorkerName, "a", "0000000000000001", soloTestJob("a").NTime, "00000000")
	}},
	{"LOWDIFF", "Share below minimum difficulty", func(t *testing.T, s *Server, c *Client) {
		c.Difficulty = 1
		nonce, _ := mineShare(t, s, soloTestJob("a"), "0000000000000001", 0, 1e-3)
		submitting(s, c, c.WorkerName, "a", "0000000000000001", soloTestJob("a").NTime, nonce)
	}},
	{"DUP", "Duplicate share rejected", func(t *testing.T, s *Server, c *Client) {
		job := soloTestJob("a")
		nonce := mineAt(t, s, job, "0000000000000001", job.NTime, 1e-6)
		for i := 0; i < 2; i++ {
			submitting(s, c, c.WorkerName, "a", "0000000000000001", job.NTime, nonce)
		}
	}},
	{"LOGIN", "Miner authorized", func(t *testing.T, s *Server, c *Client) {
		loggingIn(s, c, testPayout)
	}},
	{"LABEL", "Solo miner authorized by worker label", func(t *testing.T, s *Server, c *Client) {
		loggingIn(s, c, "rig1")
	}},
	{"OTHER-ADDRESS", "Solo miner authorized with another address as its label", func(t *testing.T, s *Server, c *Client) {
		s.config.CreditPayoutAddress = true
		loggingIn(s, c, otherAddress)
	}},
	{"PROBE", "Braiins probe connection accepted", func(t *testing.T, s *Server, c *Client) {
		s.SetSoloPayoutAddress("")
		loggingIn(s, c, "braiinstest")
	}},
	{"NO-JOB", "No job yet for a miner that just logged in: it gets the first one when it is made", func(t *testing.T, s *Server, c *Client) {
		s.currentJob = atomic.Value{} // before the first job
		loggingIn(s, c, testPayout)
	}},
	{"RENTAL", "Rental miner authorized", func(t *testing.T, s *Server, c *Client) {
		c.RentalService = RentalMRR // a pool's stratum only: a solo one never sets it
		loggingIn(s, c, testPayout)
	}},
	// The lines about a miner's difficulty are not held by the connection's own budget: the shares
	// refused before a cut can have spent it.
	{"HIGH-REJECT", "High rejection rate, reducing difficulty", func(t *testing.T, s *Server, c *Client) {
		c.Difficulty = 1
		job := soloTestJob("a")
		nonce, _ := mineShare(t, s, job, "0000000000000001", 0, 1e-3)
		for i := 0; i < 20; i++ { // 20 refused, all below the floor: the difficulty is halved
			submitting(s, c, c.WorkerName, "a", "0000000000000001", job.NTime, nonce)
		}
	}},
	{"IDLE", "Idle difficulty reset", func(t *testing.T, s *Server, c *Client) {
		c.Difficulty, c.ConnectedAt = 1, time.Now().Add(-idleResetAfter-time.Minute)
		s.clients.Store(c.ID, c)
		s.resetIdleDifficulties(time.Now())
	}},
	{"VARDIFF", "Vardiff adjusted", func(t *testing.T, s *Server, c *Client) {
		job := soloTestJob("v")
		job.NBits = "1903444b" // the network's difficulty above the miner's
		s.currentJob.Store(job)
		now := time.Now()
		c.Difficulty, c.FirstRampDone = 100000, true
		for i := 0; i < VardiffMinShares; i++ { // twice as fast as the target
			c.ShareSamples = append(c.ShareSamples, shareSample{at: now.Add(time.Duration(i-VardiffMinShares) * 5 * time.Second)})
		}
		s.adjustVardiffAt(c, now)
	}},
}

// A miner's login and the shares refused to it were left out of the log at a rental's start: lines
// about other connections had spent the port's budget. Each such line, at each place it is written,
// counts in the miners' budget instead: it is written while the port's budget is spent, and held
// once the miners' budget is.
func TestEachOfAMinersLinesIsInTheMinersBudget(t *testing.T) {
	for _, l := range minerLines {
		t.Run(l.code, func(t *testing.T) {
			s, c, logs := minerLineServer(t)
			s.logs.windowStart, s.logs.n = time.Now(), serverLogBudget
			l.cause(t, s, c)
			if n := logs.FilterMessage(l.msg).Len(); n != 1 {
				t.Errorf("LOGB-ROUTE-%s: with the port's budget spent by other connections, %q was written %d times, want once", l.code, l.msg, n)
			}

			s, c, logs = minerLineServer(t)
			s.minerLogs.at, s.minerLogs.tokens = time.Now().Add(time.Hour), 0
			l.cause(t, s, c)
			left := s.leftOut.take()[minersBudget][l.msg]
			if n := logs.FilterMessage(l.msg).Len(); n != 0 || left != 1 {
				t.Errorf("LOGB-HELD-%s: with the miners' budget spent, %q was written %d times and counted as left out %d times, want 0 and 1", l.code, l.msg, n, left)
			}
		})
	}
}

// Every refused share's line is written by s.minerLog just before the share is counted, at every
// place a share is refused, those a submit cannot reach included (a json.Number, which the decoder
// never makes). Only a share over the rate limit has no line: a miner over it sends a hundred a second.
func TestEveryRefusedSharesLineIsAMinersLine(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "server.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	sites := 0
	ast.Inspect(f, func(n ast.Node) bool {
		b, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for i, st := range b.List {
			call := serverCall(st, "noteInvalidShare")
			if call == nil || len(call.Args) != 2 {
				continue
			}
			lit, _ := call.Args[1].(*ast.BasicLit)
			reason := ""
			if lit != nil {
				reason, _ = strconv.Unquote(lit.Value)
			}
			if reason == "rate_limited" {
				continue
			}
			sites++
			if i == 0 || serverCall(b.List[i-1], "minerLog") == nil {
				t.Errorf("LOGB-REFUSED-SITE: server.go:%d counts a refused share (%s) whose line is not written by s.minerLog just before",
					fset.Position(st.Pos()).Line, reason)
			}
		}
		return true
	})
	if sites < 11 {
		t.Errorf("LOGB-REFUSED-SITES: %d places refuse a share, want the 11 there are", sites)
	}
}

// serverCall is the call st makes to s.<name>, or nil.
func serverCall(st ast.Stmt, name string) *ast.CallExpr {
	e, ok := st.(*ast.ExprStmt)
	if !ok {
		return nil
	}
	call, ok := e.X.(*ast.CallExpr)
	if !ok {
		return nil
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return nil
	}
	if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "s" {
		return nil
	}
	return call
}
