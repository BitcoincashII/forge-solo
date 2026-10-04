package stratum

import (
	"encoding/json"
	"io"
	"net"
	"testing"
)

// queuedClient is a connection whose messages stay in its queue for the test to read.
func queuedClient(t *testing.T) *Client {
	t.Helper()
	poolSide, minerSide := net.Pipe()
	t.Cleanup(func() { poolSide.Close(); minerSide.Close() })
	go io.Copy(io.Discard, minerSide)
	return &Client{ID: "q", Conn: poolSide, IP: "203.0.113.5:4000", out: make(chan []byte, clientQueue)}
}

// sent takes what is queued for c: each message's method, or "response".
func sent(c *Client) []string {
	var got []string
	for {
		select {
		case m := <-c.out:
			var msg struct {
				Method string `json:"method"`
			}
			json.Unmarshal(m, &msg)
			if msg.Method == "" {
				msg.Method = "response"
			}
			got = append(got, msg.Method)
		default:
			return got
		}
	}
}

// A connection's first login is answered with its difficulty and the current job. A repeat of it is
// answered alone: each repeat re-sent the whole job, so one 59-byte line made the stratum upload a
// job (137 KB with a large TIDES split), as often as a client cared to send it. A repeat that
// changes the difficulty still gets both, so the job is recorded under the difficulty it went out
// under.
func TestRepeatedAuthorizeIsAnsweredWithoutAnotherJob(t *testing.T) {
	s, _ := perJobServer()
	s.SetSoloPayoutAddress(testPayout)
	job := soloTestJob("1")
	s.currentJob.Store(job)
	s.jobHistory.Store(job.ID, job)
	c := queuedClient(t)
	c.Difficulty = s.config.AbsoluteMinDiff // where handleClient starts every connection
	login := func(password string) []string {
		s.handleMessage(c, []byte(`{"id":2,"method":"mining.authorize","params":["rig1","`+password+`"]}`))
		return sent(c)
	}

	if got := login("x"); len(got) != 3 || got[0] != "response" || got[1] != MethodSetDifficulty || got[2] != MethodNotify {
		t.Fatalf("AUTH-FIRST: the first login was answered with %v, want the response, the difficulty and the job", got)
	}
	for i := 0; i < 50; i++ {
		if got := login("x"); len(got) != 1 || got[0] != "response" {
			t.Fatalf("AUTH-REPEAT-WORK: repeat %d of the same login was answered with %v, want the response alone", i+1, got)
		}
	}
	if got := login("d=0.5"); len(got) != 3 || got[1] != MethodSetDifficulty || got[2] != MethodNotify {
		t.Fatalf("AUTH-REPEAT-DIFF: a repeat that changed the difficulty was answered with %v, want the response, the difficulty and the job", got)
	}
}
