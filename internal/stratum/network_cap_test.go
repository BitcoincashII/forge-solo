package stratum

import (
	"encoding/json"
	"testing"
	"time"
)

// bch2Bits is a BCH2 block's nBits (height 83361, internal/tidesgw/gateway_test.go): network
// difficulty about 1.54e9.
const bch2Bits = "1902c9b9"

// A miner sends no share below the difficulty it was given, so one given more than the network's
// keeps back every block between the two. Every way a difficulty is set stopped only at max_diff,
// 1e12 on both ports: a password d=5000000000, mining.suggest_difficulty, a remembered level and
// vardiff all gave a miner more than the network difficulty.
func TestNoMinerIsGivenADifficultyAboveTheNetwork(t *testing.T) {
	s := rentalPortServer() // the shipped rental port: floor 500000, max_diff 1e12
	s.config.SoloOnly = true
	s.SetSoloPayoutAddress(testPayout)
	job := soloTestJob("1")
	job.NBits = bch2Bits
	s.BroadcastJob(job)
	network := BitsToDifficulty(bch2Bits)

	diffOf := func(c *Client) float64 {
		c.mu.RLock()
		defer c.mu.RUnlock()
		return c.Difficulty
	}
	login := func(password string) *Client {
		c := queuedClient(t)
		c.Difficulty = s.config.AbsoluteMinDiff
		s.handleMessage(c, []byte(`{"id":2,"method":"mining.authorize","params":["rig1","`+password+`"]}`))
		return c
	}

	if d := diffOf(login("d=5000000000")); d > network {
		t.Errorf("NETCAP-HINT: a password d=5000000000 set the difficulty to %.4g, network %.4g", d, network)
	}

	c := login("x")
	s.handleMessage(c, []byte(`{"id":3,"method":"mining.suggest_difficulty","params":[5000000000]}`))
	if d := diffOf(c); d > network {
		t.Errorf("NETCAP-SUGGEST: mining.suggest_difficulty set the difficulty to %.4g, network %.4g", d, network)
	}

	s.rememberDifficulty(testPayout, "rig1", hostOf(c.IP), 2.5e11)
	if d := diffOf(login("x")); d > network {
		t.Errorf("NETCAP-RESUME: a remembered level resumed at %.4g, network %.4g", d, network)
	}

	// Vardiff raises a miner just below the network difficulty by half.
	v := login("x")
	now := time.Now()
	v.mu.Lock()
	v.Difficulty, v.FirstRampDone = 1.2e9, true
	for i := 0; i < VardiffMinShares; i++ { // twice as fast as the target
		v.ShareSamples = append(v.ShareSamples, shareSample{at: now.Add(time.Duration(i-VardiffMinShares) * 2500 * time.Millisecond)})
	}
	v.mu.Unlock()
	s.adjustVardiffAt(v, now)
	if d := diffOf(v); d > network {
		t.Errorf("NETCAP-VARDIFF: vardiff raised the difficulty to %.4g, network %.4g", d, network)
	}

	// The network difficulty falls with the next block: the miner is brought down to it, and told.
	s.clients.Store(v.ID, v)
	sent(v)
	lower := soloTestJob("2")
	lower.NBits = "1903a30c"
	s.BroadcastJob(lower)
	want := BitsToDifficulty(lower.NBits)
	if d := diffOf(v); d != want {
		t.Errorf("NETCAP-NEW-BLOCK: after the network difficulty fell to %.4g the miner is at %.4g", want, d)
	}
	told := 0.0
	for drained := false; !drained; {
		select {
		case m := <-v.out:
			var n struct {
				Method string    `json:"method"`
				Params []float64 `json:"params"`
			}
			if json.Unmarshal(m, &n) == nil && n.Method == MethodSetDifficulty && len(n.Params) == 1 {
				told = n.Params[0]
			}
		default:
			drained = true
		}
	}
	if told != want {
		t.Errorf("NETCAP-NEW-BLOCK-SENT: the miner was last told %.4g, want %.4g", told, want)
	}
}

// On a test chain the network difficulty is below the port's floor, so no difficulty can be kept
// under it there: what a miner asks for is left as it was.
func TestATestChainLeavesTheDifficultyAsItWas(t *testing.T) {
	s := rentalPortServer()
	s.config.SoloOnly = true
	s.SetSoloPayoutAddress(testPayout)
	job := soloTestJob("1")
	job.NBits = "207fffff" // regtest
	s.BroadcastJob(job)
	c := queuedClient(t)
	c.Difficulty = s.config.AbsoluteMinDiff
	s.handleMessage(c, []byte(`{"id":2,"method":"mining.authorize","params":["rig1","d=5000000"]}`))
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.Difficulty != 5000000 {
		t.Fatalf("NETCAP-TEST-CHAIN: on a test chain d=5000000 set %.4g", c.Difficulty)
	}
}

// Over a real connection: shares that arrive together lift a miner no higher than the network. It
// takes a full record of them: until then firstRamp waits for shares that agree with the time since
// the first job.
func TestSharesSentTogetherStopAtTheNetworkDifficulty(t *testing.T) {
	s := tcpServer(t, nil)
	m := dialTestMiner(t, s, "rig1")
	job := soloTestJob("1")
	job.NBits = "1f00ffff" // network difficulty 1/65536, below the 1000 times the floor they lift a miner to
	_, got := sharesTogether(t, s, m, job, maxShareSamples+1)
	if network := BitsToDifficulty(job.NBits); got > network {
		t.Fatalf("NETCAP-TCP: the miner was set to %.4g, network %.4g", got, network)
	}
}
