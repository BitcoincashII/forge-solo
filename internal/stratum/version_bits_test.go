package stratum

import (
	"encoding/json"
	"testing"
)

// A miner that does not roll versions may still send the optional sixth submit parameter: as JSON
// null, as text, with a 0x prefix. Its shares are checked against the job's own version, as
// RollVersion has always taken such a field, not refused as malformed.
func TestVersionBitsWithoutRolling(t *testing.T) {
	for _, sixth := range []interface{}{nil, "null", "0x20000000", "", "zzzzzzzz", "000000000000000000"} {
		s, cp := perJobServer()
		c := perJobClient(t)
		job := soloTestJob("a")
		s.jobHistory.Store("a", job)
		s.currentJob.Store(job)
		c.mu.Lock()
		c.Difficulty = 1e-6
		c.mu.Unlock()
		nonce, _ := mineShare(t, s, job, "0000000000000001", 1e-6, 1e300)
		params, _ := json.Marshal([]interface{}{c.WorkerName, "a", "0000000000000001", job.NTime, nonce, sixth})
		r := s.handleSubmit(c, &Request{ID: 1, Method: MethodSubmit, Params: params})
		if r.Result != true {
			t.Errorf("VERSION-BITS: sixth parameter %#v: a share with valid work was refused: %+v", sixth, r.Error)
			continue
		}
		credited(t, cp, "VERSION-BITS")
	}
}

func TestNormalizeVersionBits(t *testing.T) {
	for in, want := range map[string]string{"": "", "1fffe000": "1fffe000", "0x1fffe000": "1fffe000", "<nil>": "",
		"null": "", "123456789": "", "0X00002000": "00002000"} {
		if got := normalizeVersionBits(in); got != want {
			t.Errorf("VERSION-BITS-NORMALIZE: %q gives %q, want %q", in, got, want)
		}
	}
}
