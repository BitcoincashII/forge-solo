package stratum

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// Difficulties small enough to mine in a test -- a share at 1e-6 takes a few thousand hashes. The
// judging is the same at any scale.
const (
	jobLow  = 1e-6 // the job the miner is still working went out under this
	jobMid  = 2e-6 // the first raise since, still inside the grace window
	jobHigh = 4e-6 // the second raise: the current difficulty
)

type captureProcessor struct{ ch chan *Share }

func (c *captureProcessor) ProcessShare(_ context.Context, sh *Share) error { c.ch <- sh; return nil }
func (c *captureProcessor) ProcessBlock(context.Context, *Block) error      { return nil }

// soloTestJob is a well-formed job; its shares are real proof of work against it.
func soloTestJob(id string) *Job {
	return &Job{
		ID:            id,
		PrevBlockHash: strings.Repeat("00", 32),
		CoinBase1:     "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff1703",
		CoinBase2:     "ffffffff0100f2052a010000001976a914079b476da46a5a24bcf3250160a609c18dbcce2188ac00000000",
		Version:       "20000000",
		NBits:         "1d00ffff",
		NTime:         "6aba7069",
	}
}

func perJobServer() (*Server, *captureProcessor) {
	cp := &captureProcessor{ch: make(chan *Share, 64)}
	return &Server{
		config: &ServerConfig{ExtraNonce1Size: 4, ExtraNonce2Size: 8, MinDiff: 1, AbsoluteMinDiff: 1e-9,
			MaxDiff: 1e12, TargetShareTime: 10, RetargetTime: 30, VardiffEnabled: true, SoloOnly: true},
		logger: zap.NewNop(), stats: &ServerStats{}, shareProcessor: cp,
	}, cp
}

// perJobClient is an authorized miner whose wire is read and thrown away.
func perJobClient(t *testing.T) *Client {
	t.Helper()
	poolSide, minerSide := net.Pipe()
	t.Cleanup(func() { poolSide.Close(); minerSide.Close() })
	go io.Copy(io.Discard, minerSide)
	return &Client{ID: "c", Conn: poolSide, IP: "203.0.113.5:4000", Authorized: true, MinerID: testPayout,
		WorkerName: "rig", ExtraNonce1: "01000001", ExtraNonce2Size: 8, LastSettingsRefresh: time.Now(), SoloMining: true}
}

// toldLowThenRaisedTwice replays what a miner on a lossy link is sent: job "a" under jobLow, then
// two raises and job "b". The miner is still on job "a" and has heard neither raise.
func toldLowThenRaisedTwice(s *Server, c *Client, raisedAt time.Time) {
	s.jobHistory.Store("a", soloTestJob("a"))
	s.jobHistory.Store("b", soloTestJob("b"))
	c.mu.Lock()
	c.Difficulty = jobLow
	c.mu.Unlock()
	s.sendDifficulty(c, jobLow)
	s.sendJob(c, soloTestJob("a"))

	c.mu.Lock()
	c.PreviousDifficulty, c.DifficultyChangedAt, c.Difficulty = jobMid, raisedAt, jobHigh
	c.mu.Unlock()
	s.sendDifficulty(c, jobMid)
	s.sendDifficulty(c, jobHigh)
	s.sendJob(c, soloTestJob("b"))
}

// mineShare finds a nonce whose work on job lands in [lo, hi), with extranonce2 en2.
func mineShare(t *testing.T, s *Server, job *Job, en2 string, lo, hi float64) (string, float64) {
	t.Helper()
	for n := uint32(0); n < 1<<24; n++ {
		nonce := fmt.Sprintf("%08x", n)
		_, d, _, err := s.validateShare(job, "01000001", en2, job.NTime, nonce, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if d >= lo && d < hi {
			return nonce, d
		}
	}
	t.Fatalf("no share in [%g, %g)", lo, hi)
	return "", 0
}

func submitShare(s *Server, c *Client, jobID, en2, nonce string) *Response {
	params, _ := json.Marshal([]string{c.WorkerName, jobID, en2, soloTestJob(jobID).NTime, nonce})
	return s.handleSubmit(c, &Request{ID: 7, Method: MethodSubmit, Params: params})
}

func credited(t *testing.T, cp *captureProcessor, code string) float64 {
	t.Helper()
	select {
	case sh := <-cp.ch:
		return sh.Difficulty
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: the accepted share never reached the processor", code)
		return 0
	}
}

// A share on a job that went out under a lower difficulty is judged at that difficulty, however
// many raises the miner has not yet heard of, and credited at it -- never at the current one.
func TestShareOnAJobSentUnderALowerDifficultyIsJudgedByIt(t *testing.T) {
	s, cp := perJobServer()
	c := perJobClient(t)
	toldLowThenRaisedTwice(s, c, time.Now())

	// Work between jobLow and the grace target, on the old job: the miner found it against jobLow.
	nonce, actual := mineShare(t, s, soloTestJob("a"), "0000000000000001", jobLow, jobMid)
	if r := submitShare(s, c, "a", "0000000000000001", nonce); r.Result != true {
		t.Fatalf("PER-JOB-ACCEPT: a share proving %g on a job sent under %g was refused (%+v)", actual, jobLow, r.Error)
	}
	if got := credited(t, cp, "PER-JOB-ACCEPT"); got != jobLow {
		t.Fatalf("PER-JOB-CREDIT: credited %g, want the job's difficulty %g", got, jobLow)
	}

	// Plenty of work on the old job is still worth only the target it was found against.
	nonce, actual = mineShare(t, s, soloTestJob("a"), "0000000000000002", jobHigh, 1e9)
	if r := submitShare(s, c, "a", "0000000000000002", nonce); r.Result != true {
		t.Fatalf("PER-JOB-ACCEPT-HIGH: %+v", r.Error)
	}
	if got := credited(t, cp, "PER-JOB-ACCEPT-HIGH"); got != jobLow {
		t.Fatalf("PER-JOB-CREDIT-CAP: a share proving %g on a job sent under %g was credited %g", actual, jobLow, got)
	}

	// The same weak work on the job sent after both raises is judged as before: refused.
	nonce, actual = mineShare(t, s, soloTestJob("b"), "0000000000000003", jobLow, jobMid)
	if r := submitShare(s, c, "b", "0000000000000003", nonce); r.Result == true {
		t.Fatalf("PER-JOB-NEW-JOB: a share proving %g on a job sent under %g was accepted", actual, jobHigh)
	}

	// And full work on the new job is credited the current difficulty, as always.
	nonce, _ = mineShare(t, s, soloTestJob("b"), "0000000000000004", jobHigh, 1e9)
	if r := submitShare(s, c, "b", "0000000000000004", nonce); r.Result != true {
		t.Fatalf("PER-JOB-NEW-JOB-FULL: %+v", r.Error)
	}
	if got := credited(t, cp, "PER-JOB-NEW-JOB-FULL"); got != jobHigh {
		t.Fatalf("PER-JOB-NEW-JOB-CREDIT: credited %g, want %g", got, jobHigh)
	}
}

// A share on an old job is credited but not timed: it says nothing about the share rate at the
// current difficulty, and timing it made vardiff raise again and again while the miner was stuck.
func TestOldJobSharesDoNotDriveVardiff(t *testing.T) {
	s, cp := perJobServer()
	c := perJobClient(t)
	toldLowThenRaisedTwice(s, c, time.Now().Add(-time.Hour)) // long enough ago that vardiff may retarget
	c.mu.Lock()
	for i := VardiffMinShares; i > 0; i-- { // ten shares a millisecond apart: far faster than the target
		c.ShareTimes = append(c.ShareTimes, time.Now().Add(-time.Duration(i)*time.Millisecond))
	}
	timesBefore := len(c.ShareTimes)
	c.mu.Unlock()

	nonce, _ := mineShare(t, s, soloTestJob("a"), "0000000000000001", jobLow, 1e9)
	if r := submitShare(s, c, "a", "0000000000000001", nonce); r.Result != true {
		t.Fatalf("TIMED-OLD-JOB-ACCEPT: %+v", r.Error)
	}
	credited(t, cp, "TIMED-OLD-JOB-ACCEPT")
	c.mu.RLock()
	times, diff := len(c.ShareTimes), c.Difficulty
	c.mu.RUnlock()
	if times != timesBefore || diff != jobHigh {
		t.Fatalf("TIMED-OLD-JOB: a share on an old job was timed (%d -> %d samples) or moved the difficulty (%g -> %g)",
			timesBefore, times, jobHigh, diff)
	}

	// The same samples with a share on the current job do move it: the gate above is not vacuous.
	nonce, _ = mineShare(t, s, soloTestJob("b"), "0000000000000002", jobHigh, 1e9)
	if r := submitShare(s, c, "b", "0000000000000002", nonce); r.Result != true {
		t.Fatalf("TIMED-NEW-JOB-ACCEPT: %+v", r.Error)
	}
	credited(t, cp, "TIMED-NEW-JOB-ACCEPT")
	c.mu.RLock()
	times, diff = len(c.ShareTimes), c.Difficulty
	c.mu.RUnlock()
	if times != timesBefore+1 || !(diff > jobHigh) {
		t.Fatalf("TIMED-NEW-JOB: a share on the current job left %d samples (want %d) and difficulty %g (want a raise from %g)",
			times, timesBefore+1, diff, jobHigh)
	}
}

// judgeShare only ever lowers what a share is judged at and credited, and only for a job sent
// under a lower difficulty than the one it replaces.
func TestJudgeShareOnlyLowers(t *testing.T) {
	cases := []struct {
		name                         string
		assigned, effective, jobDiff float64
		judge, creditCap             float64
	}{
		{"no record of the job", 8192, 8192, 0, 8192, 8192},
		{"job sent under the current difficulty", 8192, 8192, 8192, 8192, 8192},
		{"job sent two raises ago", 8192, 4096, 2048, 2048, 2048},
		{"job sent after the first of two raises", 8192, 4096, 4096, 4096, 4096},
		{"job sent between the grace target and the current one", 8192, 2048, 4096, 2048, 4096},
		{"job sent before a lowering", 2048, 2048, 8192, 2048, 2048},
	}
	for _, k := range cases {
		judge, cap := judgeShare(k.assigned, k.effective, k.jobDiff)
		if judge != k.judge || cap != k.creditCap {
			t.Errorf("JUDGE-TABLE: %s: judge %g cap %g, want %g %g", k.name, judge, cap, k.judge, k.creditCap)
		}
		if judge > k.effective || cap > k.assigned {
			t.Errorf("JUDGE-RAISES: %s: judge %g (effective %g), cap %g (assigned %g)", k.name, judge, k.effective, cap, k.assigned)
		}
	}
}

// The record is bounded, and a job sent twice keeps the lower difficulty.
func TestJobDifficultyRecordIsBounded(t *testing.T) {
	c := &Client{}
	for i := 0; i <= maxJobDiffs; i++ {
		c.noteJobDifficulty(fmt.Sprintf("%x", i), float64(i+1))
	}
	if _, kept := c.jobDiff["0"]; kept || len(c.jobDiff) != maxJobDiffs || len(c.jobDiffOrder) != maxJobDiffs {
		t.Fatalf("RECORD-BOUND: %d entries, %d in order, oldest kept %v", len(c.jobDiff), len(c.jobDiffOrder), kept)
	}
	c.noteJobDifficulty("5", 1)
	c.noteJobDifficulty("5", 99)
	if c.jobDiff["5"] != 1 || len(c.jobDiffOrder) != maxJobDiffs {
		t.Fatalf("RECORD-LOWER: job sent under 6, 1 and 99 recorded as %g (%d in order)", c.jobDiff["5"], len(c.jobDiffOrder))
	}
}

// Whatever the order goroutines raise the difficulty and send jobs in, each job's record names the
// last difficulty the miner received before that job's notify.
func TestJobDifficultyRecordMatchesTheWire(t *testing.T) {
	s, _ := perJobServer()
	poolSide, minerSide := net.Pipe()
	c := &Client{ID: "c", Conn: poolSide}

	// The reader owns wire; it hands a round over by answering that round's marker line, after
	// every notify sent before the marker has been entered.
	wire := map[string]float64{}
	marks := make(chan int)
	read := make(chan struct{})
	go func() {
		defer close(read)
		r := bufio.NewReader(minerSide)
		last := 0.0
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			var m struct {
				ID     json.RawMessage
				Method string
				Params []json.RawMessage
			}
			if json.Unmarshal([]byte(line), &m) != nil {
				continue
			}
			switch m.Method {
			case MethodSetDifficulty:
				json.Unmarshal(m.Params[0], &last)
			case MethodNotify:
				var id string
				json.Unmarshal(m.Params[0], &id)
				wire[id] = last
			case "":
				var mark int
				if json.Unmarshal(m.ID, &mark) == nil && mark > 0 {
					marks <- mark
				}
			}
		}
	}()

	stop := make(chan struct{})
	var raiser sync.WaitGroup
	raiser.Add(1)
	go func() {
		defer raiser.Done()
		for d := 1.0; ; d++ {
			select {
			case <-stop:
				return
			default:
				s.sendDifficulty(c, d)
			}
		}
	}()
	mismatched, compared := 0, 0
	for round := 1; round <= 20; round++ {
		for i := 0; i < maxJobDiffs; i++ {
			s.sendJob(c, &Job{ID: fmt.Sprintf("%x-%x", round, i)})
		}
		// Compare this round before the next one evicts it from the record.
		s.sendResponse(c, &Response{ID: round, Result: true})
		if got := <-marks; got != round {
			t.Fatalf("WIRE-ORDER-MARK: round %d answered as %d", round, got)
		}
		c.mu.RLock()
		for id, d := range c.jobDiff {
			if w, seen := wire[id]; seen {
				compared++
				if w != d {
					mismatched++
				}
			}
		}
		c.mu.RUnlock()
	}
	close(stop)
	raiser.Wait()
	poolSide.Close()
	<-read
	if compared < 20*maxJobDiffs/2 {
		t.Fatalf("WIRE-ORDER-SAMPLE: only %d jobs compared", compared)
	}
	if mismatched > 0 {
		t.Fatalf("WIRE-ORDER: %d of %d jobs were recorded under a difficulty other than the last one sent before their notify",
			mismatched, compared)
	}
}

// End to end: a miner that asks for a difficulty right after authorizing -- as ESP-Miner and
// NerdQAxe do -- must be TOLD it, since that is what it is now judged by.
func TestSuggestRightAfterAuthorizeReachesTheMiner(t *testing.T) {
	s := NewServer(&ServerConfig{Host: "127.0.0.1", Port: 0, MaxConnections: 10, ExtraNonce1Size: 4, ExtraNonce2Size: 8,
		MinDiff: 1024, AbsoluteMinDiff: 1024, MaxDiff: 1e12, VardiffEnabled: true, TargetShareTime: 10, RetargetTime: 30,
		SoloOnly: true}, zap.NewNop(), nil, nil)
	s.SetSoloPayoutAddress(testPayout)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	s.BroadcastJob(soloTestJob("j1"))

	c := dialFirstByte(t, s.ListenAddr())
	r := bufio.NewReader(c)
	c.Write([]byte(`{"id":1,"method":"mining.subscribe","params":["NerdQAxe++/BM1370/v1.1.0"]}` + "\n" +
		`{"id":2,"method":"mining.authorize","params":["blocky","x"]}` + "\n" +
		`{"id":3,"method":"mining.suggest_difficulty","params":[4096]}` + "\n"))

	var told []float64
	for {
		line := readLine(t, r, c, "CONNECT-WIRE")
		var m struct {
			ID     json.RawMessage
			Method string
			Params []json.RawMessage
		}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("CONNECT-WIRE: %q", line)
		}
		if m.Method == MethodSetDifficulty {
			var d float64
			json.Unmarshal(m.Params[0], &d)
			told = append(told, d)
		}
		if strings.TrimSpace(string(m.ID)) == "3" {
			break
		}
	}
	if len(told) == 0 || told[0] == 4096 {
		t.Fatalf("CONNECT-PRECONDITION: the miner did not start below its suggestion: %v", told)
	}
	if told[len(told)-1] != 4096 {
		t.Fatalf("CONNECT-SUGGEST: the miner was last told %v, but it is now judged at 4096", told)
	}
}
