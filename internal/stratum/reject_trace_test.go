package stratum

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

const rigUA = "bosminer-plus-tuner 0.9.3-5a7fd334"

// rejectRun is one refused submit: its answer, the reasons reported for it, and what was logged.
type rejectRun struct {
	resp    *Response
	reasons []string
	logs    *observer.ObservedLogs
	server  int64 // the server's count of invalid shares
}

// refuse sets up a solo server and a rig on it, runs submit, and returns what the stratum did.
func refuse(t *testing.T, submit func(s *Server, c *Client) *Response) rejectRun {
	t.Helper()
	s, _ := perJobServer()
	core, logs := observer.New(zapcore.InfoLevel)
	s.logger = zap.New(core)
	var mu sync.Mutex
	var reasons []string
	s.SetInvalidShareHandler(func(_, _, reason string) {
		mu.Lock()
		reasons = append(reasons, reason)
		mu.Unlock()
	})
	c := perJobClient(t)
	c.UserAgent = rigUA
	c.Difficulty = 1e-6
	resp := submit(s, c)
	mu.Lock()
	defer mu.Unlock()
	return rejectRun{resp: resp, reasons: append([]string(nil), reasons...), logs: logs, server: s.stats.InvalidShares.Load()}
}

// line is the one line logged whose message contains part, or nil.
func (r rejectRun) line(part string) *observer.LoggedEntry {
	for _, e := range r.logs.All() {
		if strings.Contains(e.Message, part) {
			e := e
			return &e
		}
	}
	return nil
}

// fieldsAre checks e's fields hold want.
func fieldsAre(t *testing.T, code string, e *observer.LoggedEntry, want map[string]string) {
	t.Helper()
	if e == nil {
		t.Errorf("%s: no line logged", code)
		return
	}
	got := e.ContextMap()
	for k, v := range want {
		if s, _ := got[k].(string); s != v {
			t.Errorf("%s: field %s is %q, want %q (line %q: %v)", code, k, s, v, e.Message, got)
		}
	}
}

// When a marketplace shows a rental's rejects, the owner has to tell the kinds apart from the log:
// a share that came just after a new block, an ntime the job does not allow, a malformed submit,
// a share over the rate limit. The first three were counted but left no line, a share over the rate
// limit was not even counted, and a stale share was reported as a job not found.
func TestEveryKindOfRejectIsLoggedAndCounted(t *testing.T) {
	t.Run("stale tip", func(t *testing.T) {
		r := refuse(t, func(s *Server, c *Client) *Response {
			job, next := soloTestJob("a"), soloTestJob("b")
			next.PrevBlockHash = strings.Repeat("11", 32)
			s.jobHistory.Store("a", job)
			s.jobHistory.Store("b", next)
			s.currentJob.Store(next)
			return submitAt(s, c, "a", "0000000000000002", job.NTime, mineAt(t, s, job, "0000000000000002", job.NTime, 1e-6))
		})
		if r.resp.Error != ErrJobNotFound {
			t.Fatalf("REJECT-STALE-SETUP: %+v", r.resp.Error)
		}
		if len(r.reasons) != 1 || r.reasons[0] != "stale_tip" {
			t.Errorf("REJECT-STALE-LABEL: a share on the previous block was reported as %q, want stale_tip", r.reasons)
		}
		e := r.line("refused as stale")
		if e == nil {
			t.Fatal("REJECT-STALE-LOG: a share on the previous block left no line")
		}
		if e.Level != zapcore.InfoLevel || !strings.Contains(e.Message, "expected now and then") {
			t.Errorf("REJECT-STALE-INFO: a stale share, expected after a new block, was logged at %s as %q", e.Level, e.Message)
		}
		fieldsAre(t, "REJECT-STALE-FIELDS", e, map[string]string{"job": "a", "ip": "203.0.113.5:4000", "user_agent": rigUA})
	})

	t.Run("job not found", func(t *testing.T) {
		r := refuse(t, func(s *Server, c *Client) *Response {
			job := soloTestJob("a")
			s.jobHistory.Store("a", job)
			s.currentJob.Store(job)
			return submitAt(s, c, "ee", "0000000000000002", job.NTime, "00000000")
		})
		if len(r.reasons) != 1 || r.reasons[0] != "job_not_found" {
			t.Errorf("REJECT-NOTFOUND-LABEL: a job not found was reported as %q", r.reasons)
		}
		fieldsAre(t, "REJECT-NOTFOUND-FIELDS", r.line("Job not found"),
			map[string]string{"job": "ee", "ip": "203.0.113.5:4000", "user_agent": rigUA, "extranonce1": "01000001"})
	})

	t.Run("ntime", func(t *testing.T) {
		var late string
		r := refuse(t, func(s *Server, c *Client) *Response {
			job := soloTestJob("a")
			s.jobHistory.Store("a", job)
			s.currentJob.Store(job)
			late = ntimePlus(job.NTime, 7001)
			return submitAt(s, c, "a", "0000000000000004", late, mineAt(t, s, job, "0000000000000004", late, 1e-6))
		})
		if r.resp.Error != ErrInvalidNTime || len(r.reasons) != 1 || r.reasons[0] != "invalid_ntime" {
			t.Fatalf("REJECT-NTIME-SETUP: %+v %q", r.resp.Error, r.reasons)
		}
		e := r.line("ntime is outside")
		if e == nil || e.Level != zapcore.WarnLevel {
			t.Fatalf("REJECT-NTIME-LOG: a share with an ntime its job does not allow was not logged as a warning: %v", e)
		}
		fieldsAre(t, "REJECT-NTIME-FIELDS", e, map[string]string{"ntime": late, "job_ntime": soloTestJob("a").NTime,
			"ip": "203.0.113.5:4000", "user_agent": rigUA})
	})

	t.Run("malformed", func(t *testing.T) {
		r := refuse(t, func(s *Server, c *Client) *Response {
			job := soloTestJob("a")
			s.jobHistory.Store("a", job)
			s.currentJob.Store(job)
			return submitAt(s, c, "a", strings.Repeat("ab", 30000), job.NTime, "00000000")
		})
		if r.resp.Error != ErrMalformedShare || len(r.reasons) != 1 || r.reasons[0] != "malformed_params" {
			t.Fatalf("REJECT-MALFORMED-SETUP: %+v %q", r.resp.Error, r.reasons)
		}
		e := r.line("Malformed share refused")
		if e == nil {
			t.Fatal("REJECT-MALFORMED-LOG: a malformed submit left no line")
		}
		if en2, _ := e.ContextMap()["extranonce2"].(string); len(en2) > 40 {
			t.Errorf("REJECT-MALFORMED-CLIP: a 60 KB field was logged as %d bytes", len(en2))
		}
	})

	t.Run("over the rate limit", func(t *testing.T) {
		r := refuse(t, func(s *Server, c *Client) *Response {
			job := soloTestJob("a")
			s.jobHistory.Store("a", job)
			s.currentJob.Store(job)
			s.config.MaxSharesPerSecond = 1
			if resp := submitAt(s, c, "a", "0000000000000001", job.NTime, mineAt(t, s, job, "0000000000000001", job.NTime, 1e-6)); resp.Result != true {
				t.Fatalf("REJECT-RATE-SETUP: the first share was refused: %+v", resp.Error)
			}
			return submitAt(s, c, "a", "0000000000000002", job.NTime, mineAt(t, s, job, "0000000000000002", job.NTime, 1e-6))
		})
		if r.resp.Error != ErrRateLimited {
			t.Fatalf("REJECT-RATE-SETUP: %+v", r.resp.Error)
		}
		if r.server != 1 || len(r.reasons) != 1 || r.reasons[0] != "rate_limited" {
			t.Errorf("REJECT-RATE-COUNTED: a share refused over the rate limit counted %d, reported %q", r.server, r.reasons)
		}
	})

	t.Run("low difficulty", func(t *testing.T) {
		r := refuse(t, func(s *Server, c *Client) *Response {
			job := soloTestJob("a")
			s.jobHistory.Store("a", job)
			s.currentJob.Store(job)
			c.Difficulty = 1
			nonce, _ := mineShare(t, s, job, "0000000000000001", 0, 1e-3)
			return submitAt(s, c, "a", "0000000000000001", job.NTime, nonce)
		})
		fieldsAre(t, "REJECT-LOWDIFF-FIELDS", r.line("Share below minimum difficulty"),
			map[string]string{"ip": "203.0.113.5:4000", "user_agent": rigUA, "extranonce1": "01000001"})
	})

	// The whole share, version bits included: one result sent twice reads the same in every field.
	t.Run("duplicate", func(t *testing.T) {
		const vb = "00004000"
		var nonce string
		r := refuse(t, func(s *Server, c *Client) *Response {
			job := soloTestJob("a")
			s.jobHistory.Store("a", job)
			s.currentJob.Store(job)
			for n := uint32(0); ; n++ {
				nonce = fmt.Sprintf("%08x", n)
				if _, d, _, err := s.validateShare(job, "01000001", "0000000000000001", job.NTime, nonce, vb, 0); err == nil && d >= 1e-6 {
					break
				}
			}
			params, _ := json.Marshal([]string{c.WorkerName, "a", "0000000000000001", job.NTime, nonce, vb})
			if resp := s.handleSubmit(c, &Request{ID: 1, Method: MethodSubmit, Params: params}); resp.Result != true {
				t.Fatalf("REJECT-DUP-SETUP: %+v", resp.Error)
			}
			return s.handleSubmit(c, &Request{ID: 2, Method: MethodSubmit, Params: params})
		})
		if r.resp.Error != ErrDuplicateShare {
			t.Fatalf("REJECT-DUP-SETUP: %+v", r.resp.Error)
		}
		fieldsAre(t, "REJECT-DUP-FIELDS", r.line("Duplicate share rejected"), map[string]string{
			"job": "a", "ip": "203.0.113.5:4000", "user_agent": rigUA, "extranonce1": "01000001",
			"extranonce2": "0000000000000001", "ntime": soloTestJob("a").NTime, "nonce": nonce, "version": vb})
	})
}

// A miner's disconnect line says how many shares it sent on the connection and how long before it
// left the last one came: a rig whose own side stalled sends nothing for minutes, then goes.
func TestTheDisconnectLineSaysWhenTheLastShareCame(t *testing.T) {
	s, logs := reasonServer(t)
	conn := loggedIn(t, s)
	s.clients.Range(func(_, v interface{}) bool {
		c := v.(*Client)
		c.mu.Lock()
		c.ProvenAt = time.Now().Add(-209 * time.Second)
		c.mu.Unlock()
		c.ValidShares.Store(531)
		return true
	})
	conn.Close()
	disconnectReason(t, logs, "REJECT-DISCONNECT")
	got := logs.FilterMessage("Client disconnected").All()[0].ContextMap()
	if n, _ := got["valid_shares"].(int64); n != 531 {
		t.Errorf("REJECT-DISCONNECT-SHARES: valid_shares is %v, want 531", got["valid_shares"])
	}
	if age, _ := got["last_share_age"].(time.Duration); age < 209*time.Second || age > 220*time.Second {
		t.Errorf("REJECT-DISCONNECT-AGE: last_share_age is %v, want about 209 s", got["last_share_age"])
	}

	// No share: no age to give.
	s2, logs2 := reasonServer(t)
	loggedIn(t, s2).Close()
	disconnectReason(t, logs2, "REJECT-DISCONNECT")
	got = logs2.FilterMessage("Client disconnected").All()[0].ContextMap()
	if _, ok := got["last_share_age"]; ok || got["valid_shares"] != int64(0) {
		t.Errorf("REJECT-DISCONNECT-NO-SHARE: a miner that sent no share is logged with %v", got)
	}
}
