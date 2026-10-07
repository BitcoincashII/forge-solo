package stratum

import (
	"bufio"
	"fmt"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// A miner that reads everything it is sent is not dropped because its own requests came faster
// than the answers could be written: a pipelined burst, or a backlog read after the stratum was
// held up. The queue was taken as full the moment 64 answers waited, and on a 1-CPU host the
// writer gets no CPU while the reader works through lines it already has: a burst of 70 submits
// dropped a miner that was reading, with "it stopped reading". The reader now waits for its
// writer.
func TestABurstFromAReadingMinerIsAllAnswered(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	s := NewServer(&ServerConfig{Host: "127.0.0.1", Port: 0, MaxConnections: 10, MaxConnectionsPerIP: 10,
		MaxSharesPerSecond: 100, ExtraNonce1Size: 4, ExtraNonce2Size: 8, MinDiff: 1e-6, AbsoluteMinDiff: 1e-6,
		MaxDiff: 1e12, TargetShareTime: 10, RetargetTime: 30, VardiffEnabled: true, SoloOnly: true},
		zap.NewNop(), nil)
	s.SetSoloPayoutAddress(testPayout)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	m := dialTestMiner(t, s, "rig1")
	job := soloTestJob("1")
	s.BroadcastJob(job)

	// Suggestions that alternate are each answered with a difficulty and a response; submits with
	// one response each.
	const suggests, submits = 256, 200
	var b strings.Builder
	for i := 0; i < suggests; i++ {
		fmt.Fprintf(&b, `{"id":%d,"method":"mining.suggest_difficulty","params":[%d]}`+"\n", 1000+i, 1+i%2)
	}
	for i := 0; i < submits; i++ {
		fmt.Fprintf(&b, `{"id":%d,"method":"mining.submit","params":["rig1","1","%016x","%s","%08x"]}`+"\n",
			5000+i, i, job.NTime, i)
	}
	if _, err := m.c.Write([]byte(b.String())); err != nil {
		t.Fatal(err)
	}
	answered := 0
	m.c.SetReadDeadline(time.Now().Add(10 * time.Second))
	for answered < suggests+submits {
		line, err := m.r.ReadString('\n')
		if err != nil {
			t.Fatalf("QUEUE-BURST: a miner reading every answer was dropped after %d of %d answers: %v",
				answered, suggests+submits, err)
		}
		if strings.Contains(line, `"result"`) {
			answered++
		}
	}
}

// A peer that sends requests and stops reading is still dropped, once a write to it has waited
// as long as one may.
func TestAPeerThatStopsReadingIsStillDropped(t *testing.T) {
	s := NewServer(&ServerConfig{Host: "127.0.0.1", Port: 0, MaxConnections: 10, MaxConnectionsPerIP: 10,
		ExtraNonce1Size: 4, ExtraNonce2Size: 8, MinDiff: 1e-6, AbsoluteMinDiff: 1e-6, MaxDiff: 1e12,
		TargetShareTime: 10, RetargetTime: 30, VardiffEnabled: true, SoloOnly: true}, zap.NewNop(), nil)
	s.SetSoloPayoutAddress(testPayout)
	s.writeWait = 300 * time.Millisecond
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	job := soloTestJob("1")
	job.CoinBase1 = strings.Repeat("ab", 64<<10) // a 128 KB job: the socket's buffers fill in a few hundred
	s.BroadcastJob(job)

	c, err := net.Dial("tcp", s.ListenAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r := bufio.NewReader(c)
	c.Write([]byte(`{"id":1,"method":"mining.subscribe","params":[]}` + "\n"))
	readLine(t, r, c, "QUEUE-SUBSCRIBE")
	// Logins that change the difficulty each time are each answered with the difficulty and the job.
	var b strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, `{"id":%d,"method":"mining.authorize","params":["rig1","d=0.%d"]}`+"\n", 10+i, 1+i%2)
	}
	go c.Write([]byte(b.String())) // never read again
	for end := time.Now().Add(10 * time.Second); s.clientCount.Load() > 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("QUEUE-STOPPED-READING: a peer that stopped reading was still connected 10 s later")
		}
	}
}
