package main

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/migstatus"
	"go.uber.org/zap"
)

// How often a stratum that waits out a failed move reads the move's status, and says it is not
// mining. Variables for the tests.
var (
	moveStatusPollEvery = 10 * time.Second
	notMiningLogEvery   = 10 * time.Minute
)

// waitOutFailedMove is the stratum while the move of an earlier version's data into the new
// database has failed (internal/migstatus): a database opened now would start empty where that
// data belongs, and the dashboard asks the user what to do. It opens no database, listens on no
// port and mines nothing, and says so every notMiningLogEvery. It returns, and the program ends
// with exit code 0, when it is told to stop or once the move is no longer failed; Docker's restart
// policy, or the Windows launcher, then starts it normally.
func waitOutFailedMove(db string, st migstatus.Status) {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)
	if os.Getenv("FORGE_STOP_ON_STDIN_EOF") == "1" {
		go stopOnEOF(os.Stdin, stop)
	}
	say := func(st migstatus.Status) {
		logger.Warn("not mining: moving the data to the new database failed (see the dashboard)",
			zap.Int("code", st.Code), zap.String("reason", st.Reason))
	}
	say(st)
	poll := time.NewTicker(moveStatusPollEvery)
	defer poll.Stop()
	again := time.NewTicker(notMiningLogEvery)
	defer again.Stop()
	for {
		select {
		case <-stop:
			logger.Info("Shutting down...")
			return
		case <-again.C:
			say(st)
		case <-poll.C:
			now, blocked := migstatus.Blocked(db)
			if !blocked {
				logger.Info("The move of the old data is no longer failed: stopping, to be started again normally",
					zap.String("state", now.State))
				return
			}
			st = now
		}
	}
}
