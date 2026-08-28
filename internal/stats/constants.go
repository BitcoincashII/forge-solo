package stats

import "time"

// Mining constants
const (
	// COINBASE_MATURITY is the number of confirmations required before
	// block rewards can be spent (BCH2 uses same as Bitcoin Cash)
	COINBASE_MATURITY = 100

	// DBTimeout is the default timeout for database operations
	DBTimeout = 30 * time.Second

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
