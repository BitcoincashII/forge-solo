package main

import (
	"time"

	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
)

// internalWorkers is the answer of /internal/workers: the workers the dashboard lists, and each
// miner's all-time best share (miner_ath_diff), which outlives the workers listed: one not seen
// since the last restart, or for a day, is not listed.
func internalWorkers() map[string]interface{} {
	var connected []stratum.WorkerRef
	for _, srv := range []*stratum.Server{stratumServer, stratumRentalServer} {
		if srv != nil {
			connected = append(connected, srv.AuthorizedWorkers()...)
		}
	}
	return map[string]interface{}{
		"workers":        withConnectedWorkers(stats.GetManager().GetAllWorkerStats(), connected),
		"miner_ath_diff": stats.GetManager().MinerBests(),
	}
}

// withConnectedWorkers is the worker list the dashboard shows: a worker connected and authorized
// now is online, and one with no share yet is listed too, at 0 H/s. "Online" was "a share in the
// last 5 minutes", so a small miner -- one share every 10 minutes at the lowest difficulty, or
// none for days -- showed offline most of the time, or not at all, while it worked.
//
// A worker connected now is listed with the time its connection began (its earliest, when it has
// several). One with no share yet was listed as connected in the year 1. A worker not connected
// keeps the time its first share was counted. One with no share in this run has the best share
// kept for it.
func withConnectedWorkers(workers []*stats.WorkerStats, connected []stratum.WorkerRef) []*stats.WorkerStats {
	type key struct{ miner, worker string }
	listed := make(map[key]bool, len(workers))
	since := make(map[key]time.Time, len(connected))
	for _, c := range connected {
		k := key{c.MinerID, c.WorkerName}
		if at, ok := since[k]; !ok || (!c.ConnectedAt.IsZero() && (at.IsZero() || c.ConnectedAt.Before(at))) {
			since[k] = c.ConnectedAt
		}
	}
	for _, w := range workers {
		k := key{w.MinerID, w.WorkerName}
		listed[k] = true
		if at, live := since[k]; live {
			w.Online = true
			if !at.IsZero() {
				w.ConnectedAt = at
			}
		}
	}
	for _, c := range connected {
		k := key{c.MinerID, c.WorkerName}
		if !listed[k] {
			listed[k] = true
			workers = append(workers, &stats.WorkerStats{MinerID: c.MinerID, WorkerName: c.WorkerName, Online: true, ConnectedAt: since[k],
				ATHDiff: stats.GetManager().KeptBest(c.MinerID, c.WorkerName)})
		}
	}
	return workers
}
