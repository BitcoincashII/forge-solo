package stratum

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// A miner that has gone without closing its connection (a power cut, Wi-Fi dropped) must not hold
// up the others' jobs. The broadcast wrote to each miner in turn and waited up to 10 seconds on
// each, so once such a miner's buffers were full every new-block job reached everyone else about
// that much later.
func TestOneSilentMinerDoesNotHoldUpTheBroadcast(t *testing.T) {
	s := NewServer(&ServerConfig{Host: "127.0.0.1", Port: 0, MaxConnections: 10, ExtraNonce1Size: 4, ExtraNonce2Size: 8, SoloOnly: true}, zap.NewNop(), nil, nil)
	s.SetSoloPayoutAddress(testPayout)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)

	login := func(name string) (net.Conn, *bufio.Reader) {
		c := dialFirstByte(t, s.ListenAddr())
		r := bufio.NewReaderSize(c, 1<<20)
		c.Write([]byte(`{"id":1,"method":"mining.subscribe","params":[]}` + "\n"))
		readLine(t, r, c, "BCAST-SUBSCRIBE")
		c.Write([]byte(`{"id":2,"method":"mining.authorize","params":["` + name + `","x"]}` + "\n"))
		return c, r
	}
	healthy, hr := login("healthy")
	login("silent") // never read again
	deadline := time.Now().Add(3 * time.Second)
	for {
		n := 0
		s.clients.Range(func(_, v interface{}) bool {
			c := v.(*Client)
			c.mu.RLock()
			if c.Authorized {
				n++
			}
			c.mu.RUnlock()
			return true
		})
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("BCAST-SETUP: %d miners authorized, want 2", n)
		}
		time.Sleep(20 * time.Millisecond)
	}

	const jobs = 100
	got := make(chan []string, 1)
	go func() {
		var ids []string
		healthy.SetReadDeadline(time.Now().Add(30 * time.Second))
		for len(ids) < jobs {
			line, err := hr.ReadString('\n')
			if err != nil {
				break
			}
			var n struct {
				Method string            `json:"method"`
				Params []json.RawMessage `json:"params"`
			}
			if json.Unmarshal([]byte(line), &n) == nil && n.Method == MethodNotify && len(n.Params) > 0 {
				var id string
				json.Unmarshal(n.Params[0], &id)
				ids = append(ids, id)
			}
		}
		got <- ids
	}()

	big := strings.Repeat("ab", 64<<10) // a 128 KB coinbase part: 100 jobs fill any socket buffer
	start := time.Now()
	for i := 1; i <= jobs; i++ {
		s.BroadcastJob(&Job{ID: fmt.Sprintf("%x", i), PrevBlockHash: strings.Repeat("00", 32), CoinBase1: big, CoinBase2: "00",
			Version: "20000000", NBits: "1d00ffff", NTime: "6aba7069"})
		// Jobs go out one at a time, at least a second apart in service. With no pause at all, a
		// run on one CPU gives the reading miner here, a goroutine of this test, no time to read.
		time.Sleep(time.Millisecond)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("BCAST-NOT-HELD: %d broadcasts took %v with one miner not reading", jobs, took)
	}
	ids := <-got
	if len(ids) != jobs {
		t.Fatalf("BCAST-ORDER: the reading miner got %d of %d jobs", len(ids), jobs)
	}
	for i, id := range ids {
		if id != fmt.Sprintf("%x", i+1) {
			t.Fatalf("BCAST-ORDER: job %d arrived as %q, want %q (out of order)", i+1, id, fmt.Sprintf("%x", i+1))
		}
	}
}

// Messages are queued in the order they are sent, by the sender itself: the queue's single writer
// then keeps that order on the wire. A job and the difficulty it is judged by depend on it.
func TestSendsAreQueuedInOrderAtOnce(t *testing.T) {
	s, _ := perJobServer()
	c := perJobClient(t)
	c.out = make(chan []byte, clientQueue) // a queue with no writer: what is in it stays there
	s.sendNotification(c, &Notification{Method: MethodNotify, Params: []interface{}{"1"}})
	s.sendResponse(c, &Response{ID: 2, Result: true})
	s.sendNotification(c, &Notification{Method: MethodNotify, Params: []interface{}{"3"}})
	if len(c.out) != 3 {
		t.Fatalf("BCAST-SYNC: %d of 3 messages queued when the sends returned", len(c.out))
	}
	for _, want := range []string{`"1"`, `"id":2`, `"3"`} {
		if got := string(<-c.out); !strings.Contains(got, want) {
			t.Fatalf("BCAST-SYNC: queued %s, want the message with %s next", got, want)
		}
	}
}

// A miner whose queue is full has stopped reading: it is disconnected, not waited for.
func TestAFullQueueDisconnects(t *testing.T) {
	s, _ := perJobServer()
	c := perJobClient(t)
	c.out = make(chan []byte, clientQueue)
	for i := 0; i <= clientQueue; i++ {
		s.sendNotification(c, &Notification{Method: MethodNotify, Params: []interface{}{fmt.Sprint(i)}})
	}
	c.outMu.Lock()
	closed := c.outClosed
	c.outMu.Unlock()
	if !closed {
		t.Fatal("BCAST-FULL-DROPS: a miner whose queue filled is still connected")
	}
	if _, err := c.Conn.Write([]byte("x")); err == nil {
		t.Fatal("BCAST-FULL-DROPS: the connection of a miner whose queue filled is still open")
	}
}
