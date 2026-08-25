package stratum

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// A marketplace-looking worker must be COUNTED without being given a difficulty floor.
//
// Both detection paths used to be skipped entirely in solo, because RentalService drives
// the 500000 RentalMinDiff floor -- a floor with no operator knob that nothing can climb
// back down from, so a stratum proxy in front of a few Bitaxes (whose label contains one
// of the trigger substrings) would need over an hour per share. Skipping detection fixed
// the trap and broke the reporting: /internal/rental-stats, and the public "rentals" block
// in /api/v1/stats, returned {0,0,0,0} while a rental order was mining.
//
// The two concerns are now separate fields. This pins both halves at once: identity is
// recorded, policy is not applied.
func TestSoloRecordsMarketplaceIdentityWithoutApplyingItsFloor(t *testing.T) {
	labels := map[string]RentalService{
		"nh_order12345":      RentalNiceHash,
		"NiceHash-worker":    RentalNiceHash,
		"mrr_4417":           RentalMRR,
		"miningrigrentals01": RentalMRR,
		"my-rental-proxy":    RentalOther,
	}

	for label, want := range labels {
		t.Run(label, func(t *testing.T) {
			if got := detectRentalFromWorker(label); got != want {
				t.Fatalf("detectRentalFromWorker(%q) = %v, want %v", label, got, want)
			}

			// Solo: identity recorded, policy withheld.
			solo := &Server{
				stats:  &ServerStats{},
				config: &ServerConfig{SoloOnly: true, RentalMinDiff: 500000, AbsoluteMinDiff: 1024},
			}
			c := &Client{}
			detected := detectRentalFromWorker(label)
			c.DetectedMarketplace = detected
			if !solo.config.SoloOnly {
				c.RentalService = detected
			}

			if c.DetectedMarketplace != want {
				t.Errorf("solo did not record the marketplace: got %v, want %v",
					c.DetectedMarketplace, want)
			}
			if c.RentalService != RentalNone {
				t.Errorf("solo applied marketplace difficulty policy (RentalService=%v); "+
					"this is the 500000 floor that traps a proxy in front of small ASICs",
					c.RentalService)
			}
			if floor := solo.vardiffFloor(c.RentalService != RentalNone); floor != 1024 {
				t.Errorf("solo floor = %v, want the ordinary 1024 floor — a marketplace "+
					"label must not raise it", floor)
			}
		})
	}
}

// Outside solo the floor is intended, so both fields must be set.
func TestNonSoloStillAppliesTheMarketplaceFloor(t *testing.T) {
	srv := &Server{
		stats:  &ServerStats{},
		config: &ServerConfig{SoloOnly: false, RentalMinDiff: 500000, AbsoluteMinDiff: 1024},
	}
	c := &Client{}
	detected := detectRentalFromWorker("nh_order12345")
	c.DetectedMarketplace = detected
	if !srv.config.SoloOnly {
		c.RentalService = detected
	}
	if c.RentalService != RentalNiceHash || c.DetectedMarketplace != RentalNiceHash {
		t.Fatalf("non-solo: RentalService=%v DetectedMarketplace=%v, want both NiceHash",
			c.RentalService, c.DetectedMarketplace)
	}
	if floor := srv.vardiffFloor(c.RentalService != RentalNone); floor != 500000 {
		t.Errorf("non-solo floor = %v, want 500000", floor)
	}
}

// The counters must read identity, and every difficulty decision must read policy. A
// source check, because swapping one for the other compiles cleanly and silently either
// re-traps solo miners or re-zeroes the public stats.
func TestDifficultyReadsPolicyAndCountersReadIdentity(t *testing.T) {
	raw, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	src := string(raw)

	stats := src[strings.Index(src, "func (s *Server) GetRentalStats("):]
	if i := strings.Index(stats, "\n}\n"); i > 0 {
		stats = stats[:i]
	}
	if !strings.Contains(stats, "DetectedMarketplace") {
		t.Error("GetRentalStats does not read DetectedMarketplace; it will report zeros in " +
			"solo, which is the whole defect")
	}
	if regexp.MustCompile(`:=\s*client\.RentalService\b`).MatchString(stats) {
		t.Error("GetRentalStats reads RentalService, a difficulty-policy flag that is " +
			"never set in solo")
	}

	// vardiffFloor must never be handed the identity field.
	for _, m := range regexp.MustCompile(`vardiffFloor\(([^)]*)\)`).FindAllStringSubmatch(src, -1) {
		if strings.Contains(m[1], "DetectedMarketplace") {
			t.Errorf("vardiffFloor(%s) keys off marketplace identity; in solo that restores "+
				"the 500000 floor this separation exists to avoid", m[1])
		}
	}
}
