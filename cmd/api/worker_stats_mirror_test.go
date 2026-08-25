package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/stats"
)

// cmd/api mirrors internal/stats.WorkerStats to decode the stratum's /internal/workers
// payload. The two are joined only by their JSON tags, so a field added on the stratum
// side and forgotten here decodes as a silent zero -- which is precisely how a tile ends
// up reporting a confident, wrong number rather than failing loudly.
func TestWorkerStatsMirrorsTheStratumPayload(t *testing.T) {
	tags := func(v interface{}) map[string]string {
		out := map[string]string{}
		ty := reflect.TypeOf(v)
		for i := 0; i < ty.NumField(); i++ {
			f := ty.Field(i)
			tag := f.Tag.Get("json")
			if tag == "" || tag == "-" {
				continue
			}
			out[tag] = f.Type.String()
		}
		return out
	}

	src := tags(stats.WorkerStats{})
	dst := tags(WorkerStats{})

	for tag, typ := range src {
		got, ok := dst[tag]
		if !ok {
			t.Errorf("cmd/api WorkerStats is missing %q (%s); the stratum sends it and this "+
				"side will decode a zero, so anything the dashboard computes from it is wrong "+
				"without ever erroring", tag, typ)
			continue
		}
		if got != typ {
			t.Errorf("field %q: stratum sends %s, cmd/api decodes into %s", tag, typ, got)
		}
	}
}

// And the field this test was written for must actually survive a round trip.
func TestRoundSharesSurvivesTheWire(t *testing.T) {
	b, err := json.Marshal(stats.WorkerStats{ValidShares: 900, RoundShares: 7})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got WorkerStats
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.RoundShares != 7 {
		t.Fatalf("roundShares came across as %d, want 7 — the Round Shares tile falls back "+
			"to zero (or to the all-time count) for every miner", got.RoundShares)
	}
	if got.ValidShares != 900 {
		t.Fatalf("validShares = %d, want 900", got.ValidShares)
	}
}
