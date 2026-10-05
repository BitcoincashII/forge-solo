package main

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// A config without stratum_rental.vardiff.target_time read 0: every share looked slow, and vardiff
// walked every rental down to its floor. Without stratum_rental.vardiff.min_diff that floor was
// 32768, below the 500000 NiceHash and MiningRigRentals require. Both default to what every
// platform ships (docker/stratum/config.template.yaml, which Windows and Linux are tested to match).
func TestRentalVardiffHasTheShippedDefaults(t *testing.T) {
	raw, err := os.ReadFile("../../docker/stratum/config.template.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var shipped struct {
		Rental struct {
			Vardiff struct {
				TargetTime int     `yaml:"target_time"`
				MinDiff    float64 `yaml:"min_diff"`
			} `yaml:"vardiff"`
		} `yaml:"stratum_rental"`
	}
	if err := yaml.Unmarshal(raw, &shipped); err != nil {
		t.Fatal(err)
	}
	if shipped.Rental.Vardiff.TargetTime != 25 || shipped.Rental.Vardiff.MinDiff != 500000 {
		t.Fatalf("RENTAL-SHIPPED: the template's rental vardiff is target_time %d, min_diff %g; want 25 and 500000",
			shipped.Rental.Vardiff.TargetTime, shipped.Rental.Vardiff.MinDiff)
	}

	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("stratum_rental:\n  enabled: true\n  port: 3335\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.GetInt("stratum_rental.vardiff.target_time"); got != shipped.Rental.Vardiff.TargetTime {
		t.Errorf("RENTAL-TARGET-DEFAULT: stratum_rental.vardiff.target_time defaults to %d, the template ships %d",
			got, shipped.Rental.Vardiff.TargetTime)
	}
	if got := v.GetFloat64("stratum_rental.vardiff.min_diff"); got != shipped.Rental.Vardiff.MinDiff {
		t.Errorf("RENTAL-FLOOR-DEFAULT: stratum_rental.vardiff.min_diff defaults to %g, the template ships %g",
			got, shipped.Rental.Vardiff.MinDiff)
	}
}
