package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/stratum"
)

// The rental port's listener gets what every platform ships (docker/stratum/config.template.yaml,
// which the Windows and Linux configs are tested to match). The template sets no variance_percent
// for it, so it keeps stratum.VardiffVariancePercent, as it always has.
func TestRentalListenerHasTheShippedConfig(t *testing.T) {
	v, err := loadConfig("../../docker/stratum/config.template.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := stratum.ServerConfig{Host: "0.0.0.0", Port: 3335, MaxConnections: 64, MaxConnectionsPerIP: 32,
		MaxSharesPerSecond: 100, VardiffEnabled: true, MinDiff: 500000, MaxDiff: 1e12, TargetShareTime: 25,
		RetargetTime: 10, ExtraNonce1Size: 4, ExtraNonce2Size: 8, ServerName: "rental", IsRentalPort: true,
		SoloOnly: true, CreditPayoutAddress: true}
	if got := rentalServerConfig(v); *got != want {
		t.Errorf("RENTAL-CONFIG-SHIPPED: the rental listener is configured\n%+v\nwant\n%+v", *got, want)
	}
}

// stratum_rental.vardiff.variance_percent did nothing: the rental port read no variance_percent
// and kept +/-30% whatever it said, while the main port read its own. It is read as the main port
// reads its own, a percentage.
func TestRentalListenerReadsItsVariancePercent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	cfg := "stratum:\n  vardiff:\n    variance_percent: 25\nstratum_rental:\n  enabled: true\n  vardiff:\n    variance_percent: 40\n"
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := rentalServerConfig(v).VariancePercent; math.Abs(got-0.40) > 1e-12 {
		t.Errorf("RENTAL-VARIANCE-READ: stratum_rental.vardiff.variance_percent 40 gave the rental listener a dead-band of %g, want 0.40", got)
	}
}
