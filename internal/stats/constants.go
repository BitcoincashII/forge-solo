package stats

import (
	"sync/atomic"
	"time"
)

// Mining constants
const (
	// COINBASE_MATURITY is the number of confirmations required before
	// block rewards can be spent (BCH2 uses same as Bitcoin Cash)
	COINBASE_MATURITY = 100

	// DBTimeout is the default timeout for database operations
	DBTimeout = 30 * time.Second

	// DashboardReadTimeout bounds a read made for the dashboard. The api waits 10 s for the
	// stratum, so a stalled database is reported as one within it, not as a mining service that
	// does not answer.
	DashboardReadTimeout = 5 * time.Second

	// PingTimeout bounds IsDBConnected: a stalled database answered no ping, and every caller
	// (the health check, the stratum's settings loop) waited with it.
	PingTimeout = 3 * time.Second

	// MaxPayoutBatch is the maximum number of payouts to process in one transaction
	MaxPayoutBatch = 100

	// WorkerOfflineThreshold is how long without shares before a worker is marked offline
	WorkerOfflineThreshold = 5 * time.Minute

	// ShareHistoryDuration is how long to keep share records in memory
	ShareHistoryDuration = time.Hour

	// MaxSharesPerWorker is the maximum shares to keep per worker in memory
	MaxSharesPerWorker = 10000

	// WorkerRetention is how long a silent worker is kept before it is dropped from the
	// stats map entirely. Long enough that the dashboard still lists a rig that went down
	// overnight; short enough that rotating worker names cannot grow the map without bound.
	WorkerRetention = 24 * time.Hour
)

// minerSettingsLogged is the miner-settings count last logged, plus one (zero: never logged). The
// api reloads the settings every 10 s, and logging every reload filled its log with the same line,
// about a megabyte a day.
var minerSettingsLogged atomic.Int64
