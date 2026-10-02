package main

import (
	"github.com/BitcoincashII/forge-solo/internal/stats"
	"github.com/BitcoincashII/forge-solo/internal/stratum"
)

// withConnectedWorkers is the worker list the dashboard shows: a worker connected and authorized
// now is online, and one with no share yet is listed too, at 0 H/s. "Online" was "a share in the
// last 5 minutes", so a small miner -- one share every 10 minutes at the lowest difficulty, or
// none for days -- showed offline most of the time, or not at all, while it worked.
func withConnectedWorkers(workers []*stats.WorkerStats, connected []stratum.WorkerRef) []*stats.WorkerStats {
	type key struct{ miner, worker string }
	listed := make(map[key]bool, len(workers))
	live := make(map[key]bool, len(connected))
	for _, c := range connected {
		live[key{c.MinerID, c.WorkerName}] = true
	}
	for _, w := range workers {
		k := key{w.MinerID, w.WorkerName}
		listed[k] = true
		if live[k] {
			w.Online = true
		}
	}
	for _, c := range connected {
		k := key{c.MinerID, c.WorkerName}
		if !listed[k] {
			listed[k] = true
			workers = append(workers, &stats.WorkerStats{MinerID: c.MinerID, WorkerName: c.WorkerName, Online: true})
		}
	}
	return workers
}
