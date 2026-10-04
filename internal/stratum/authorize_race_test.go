package stratum

import (
	"sync"
	"testing"
	"time"
)

// A login reads the miner and its difficulty under the client's lock, so it may run while the owner
// saves a new payout address or the idle rescue resets the connection. Run with -race: the login
// read both after the lock was let go, while the address watcher and the idle rescue wrote them.
func TestAuthorizeWhileThePayoutChangesOrTheIdleRescueRuns(t *testing.T) {
	s, _ := perJobServer()
	s.config.CreditPayoutAddress = true
	s.SetSoloPayoutAddress(testPayout)
	s.currentJob.Store(soloTestJob("1"))
	c := perJobClient(t)
	c.ConnectedAt = time.Now().Add(-time.Hour)
	s.clients.Store(c.ID, c)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				s.SetSoloPayoutAddress(otherAddress)
			} else {
				s.SetSoloPayoutAddress(testPayout)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			s.resetIdleDifficulties(time.Now())
		}
	}()
	for i := 0; i < 2000; i++ {
		// d=0.5 puts the connection above its floor with no share, so the idle rescue resets it.
		s.handleMessage(c, []byte(`{"id":2,"method":"mining.authorize","params":["rig1","d=0.5"]}`))
	}
	close(stop)
	wg.Wait()
}
