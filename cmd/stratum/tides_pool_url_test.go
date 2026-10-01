package main

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// TIDES with a plain-http pool would let anyone on the way rewrite the payout split, so Forge Solo
// refuses it; applyTidesMode then keeps mining solo.
func TestTidesRefusesPlainHTTPPool(t *testing.T) {
	if tidesGateway() != nil {
		t.Skip("a TIDES gateway is already running in this test binary")
	}
	t.Setenv("DATUM_POOL_URL", "http://pool.example")
	err := ensureTidesGateway(viper.New())
	if err == nil || !strings.Contains(err.Error(), "must use https://") {
		t.Fatalf("ensureTidesGateway with a plain-http pool = %v, want a refusal", err)
	}
	if tidesGateway() != nil {
		t.Fatal("a TIDES gateway was started anyway")
	}
}
