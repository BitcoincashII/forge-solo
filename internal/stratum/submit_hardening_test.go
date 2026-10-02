package stratum

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func recordedShares(s *Server) int {
	n := 0
	s.submittedShares.Range(func(_, _ interface{}) bool { n++; return true })
	return n
}

// submitAt sends a submit with any ntime and extranonce2, as a miner can.
func submitAt(s *Server, c *Client, jobID, en2, ntime, nonce string) *Response {
	params, _ := json.Marshal([]string{c.WorkerName, jobID, en2, ntime, nonce})
	return s.handleSubmit(c, &Request{ID: 9, Method: MethodSubmit, Params: params})
}

// mineAt finds a nonce whose work on job, at ntime, reaches at least min.
func mineAt(t *testing.T, s *Server, job *Job, en2, ntime string, min float64) string {
	t.Helper()
	for n := uint32(0); n < 1<<24; n++ {
		nonce := fmt.Sprintf("%08x", n)
		if _, d, _, err := s.validateShare(job, "01000001", en2, ntime, nonce, "", 0); err == nil && d >= min {
			return nonce
		}
	}
	t.Fatalf("no share at %s reaching %g", ntime, min)
	return ""
}

func ntimePlus(base string, add uint64) string {
	v, _ := strconv.ParseUint(base, 16, 32)
	return fmt.Sprintf("%08x", v+add)
}

// A submit is read only as far as its form allows: an oversized or junk field is refused before it
// is looked up, logged or remembered. The duplicate record used to keep every submit's strings for
// minutes, whatever their size, so a client could hold memory in proportion to what it sent.
func TestMalformedAndRefusedSubmitsAreNotRemembered(t *testing.T) {
	s, _ := perJobServer()
	c := perJobClient(t)
	job := soloTestJob("a")
	s.jobHistory.Store("a", job)
	s.currentJob.Store(job)
	c.mu.Lock()
	c.Difficulty = 1e-6
	c.mu.Unlock()

	if r := submitAt(s, c, "a", strings.Repeat("ab", 30000), job.NTime, "00000000"); r.Result != false || r.Error != ErrMalformedShare {
		t.Fatalf("SUBMIT-OVERSIZE: a 60 KB extranonce2 got %+v, want %v", r, ErrMalformedShare)
	}
	for _, bad := range [][]string{
		{strings.Repeat("f", 17), "0000000000000001", job.NTime, "00000000"}, // job id too long
		{"zz", "0000000000000001", job.NTime, "00000000"},                    // job id not hex
		{"a", "000000000000000g", job.NTime, "00000000"},                     // extranonce2 not hex
		{"a", "0000000000000001", job.NTime, "0000000000"},                   // nonce too long
	} {
		if r := submitAt(s, c, bad[0], bad[1], bad[2], bad[3]); r.Error != ErrMalformedShare {
			t.Errorf("SUBMIT-MALFORMED: %q got %+v, want %v", bad, r.Error, ErrMalformedShare)
		}
	}
	// Well-formed junk: refused on its proof of work, over the intake limit or not.
	s.config.MaxSharesPerSecond = 2
	for i := 0; i < 50; i++ {
		submitAt(s, c, "a", fmt.Sprintf("%016x", i+1), job.NTime, "ffffffff")
	}
	if n := recordedShares(s); n != 0 {
		t.Fatalf("SUBMIT-REMEMBERED: %d refused submits were kept in the duplicate record", n)
	}
}

// A share that passed is remembered for as long as its block's jobs are accepted, however long that
// is, and a share on a job from before the last block is refused as stale.
func TestDuplicatesAreCaughtForTheWholeBlockAndStaleSharesRefused(t *testing.T) {
	s, cp := perJobServer()
	c := perJobClient(t)
	job := soloTestJob("a")
	s.jobHistory.Store("a", job)
	s.currentJob.Store(job)
	c.mu.Lock()
	c.Difficulty = 1e-6
	c.mu.Unlock()

	nonce := mineAt(t, s, job, "0000000000000001", job.NTime, 1e-6)
	if r := submitAt(s, c, "a", "0000000000000001", job.NTime, nonce); r.Result != true {
		t.Fatalf("SUBMIT-VALID: %+v", r.Error)
	}
	credited(t, cp, "SUBMIT-VALID")
	if r := submitAt(s, c, "a", "0000000000000001", job.NTime, nonce); r.Error != ErrDuplicateShare {
		t.Fatalf("SUBMIT-DUP: a resubmitted share got %+v, want %v", r.Error, ErrDuplicateShare)
	}
	// The periodic prune keeps the current block's record: the resubmit is still a duplicate.
	s.cleanupOldShares()
	if r := submitAt(s, c, "a", "0000000000000001", job.NTime, nonce); r.Error != ErrDuplicateShare {
		t.Fatalf("SUBMIT-DUP-KEPT: after the periodic prune a resubmitted share got %+v, want %v", r.Error, ErrDuplicateShare)
	}

	// A new block: job "b" builds on another tip.
	next := soloTestJob("b")
	next.PrevBlockHash = strings.Repeat("11", 32)
	s.jobHistory.Store("b", next)
	s.currentJob.Store(next)
	nonce2 := mineAt(t, s, job, "0000000000000002", job.NTime, 1e-6)
	if r := submitAt(s, c, "a", "0000000000000002", job.NTime, nonce2); r.Error != ErrJobNotFound {
		t.Fatalf("SUBMIT-STALE: a share on the previous block's job got %+v, want %v", r.Error, ErrJobNotFound)
	}
	s.clearSharesForJob()
	if n := recordedShares(s); n != 0 {
		t.Fatalf("SUBMIT-PRUNE-OLD-TIP: %d records of the previous block's shares kept", n)
	}
}

// A block header counts once, whatever job it is submitted under. Two jobs on one block can carry
// identical work (the coinbase holds nothing per job), so one proof of work rolled to a time both
// accept builds the same header under either job id.
func TestOneHeaderCountsOnceWhateverItsJob(t *testing.T) {
	s, cp := perJobServer()
	c := perJobClient(t)
	first, again := soloTestJob("a"), soloTestJob("b") // the same work under two ids
	s.jobHistory.Store("a", first)
	s.jobHistory.Store("b", again)
	s.currentJob.Store(again)
	c.mu.Lock()
	c.Difficulty = 1e-6
	c.mu.Unlock()

	nonce := mineAt(t, s, first, "0000000000000001", first.NTime, 1e-6)
	if r := submitAt(s, c, "a", "0000000000000001", first.NTime, nonce); r.Result != true {
		t.Fatalf("DEDUP-FIRST: %+v", r.Error)
	}
	credited(t, cp, "DEDUP-FIRST")
	if r := submitAt(s, c, "b", "0000000000000001", first.NTime, nonce); r.Error != ErrDuplicateShare {
		t.Fatalf("DEDUP-CROSS-JOB: the same header under the next job got %+v, want %v", r.Error, ErrDuplicateShare)
	}
	// Other work under the second id is still accepted: the record holds headers, not jobs.
	other := mineAt(t, s, again, "0000000000000002", again.NTime, 1e-6)
	if r := submitAt(s, c, "b", "0000000000000002", again.NTime, other); r.Result != true {
		t.Fatalf("DEDUP-CROSS-JOB-OTHER: a different share under the second job got %+v, want accepted", r.Error)
	}
	credited(t, cp, "DEDUP-CROSS-JOB-OTHER")
}

// ntime may roll forward up to 7000 seconds from the job's, and never back (ckpool's bounds).
func TestShareNTimeBounds(t *testing.T) {
	s, cp := perJobServer()
	c := perJobClient(t)
	job := soloTestJob("a")
	s.jobHistory.Store("a", job)
	s.currentJob.Store(job)
	c.mu.Lock()
	c.Difficulty = 1e-6
	c.mu.Unlock()

	before := fmt.Sprintf("%08x", func() uint64 { v, _ := strconv.ParseUint(job.NTime, 16, 32); return v - 1 }())
	if r := submitAt(s, c, "a", "0000000000000003", before, mineAt(t, s, job, "0000000000000003", before, 1e-6)); r.Error != ErrInvalidNTime {
		t.Fatalf("SUBMIT-NTIME-BEFORE: %+v, want %v", r.Error, ErrInvalidNTime)
	}
	tooLate := ntimePlus(job.NTime, 7001)
	if r := submitAt(s, c, "a", "0000000000000004", tooLate, mineAt(t, s, job, "0000000000000004", tooLate, 1e-6)); r.Error != ErrInvalidNTime {
		t.Fatalf("SUBMIT-NTIME-LATE: %+v, want %v", r.Error, ErrInvalidNTime)
	}
	edge := ntimePlus(job.NTime, 7000)
	if r := submitAt(s, c, "a", "0000000000000005", edge, mineAt(t, s, job, "0000000000000005", edge, 1e-6)); r.Result != true {
		t.Fatalf("SUBMIT-NTIME-EDGE: a share rolled 7000 s forward got %+v, want accepted", r.Error)
	}
	credited(t, cp, "SUBMIT-NTIME-EDGE")
}
