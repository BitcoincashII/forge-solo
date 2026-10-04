package main

import (
	"net"
	"strconv"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/stratum"
	"go.uber.org/zap"
)

func rentalServerOn(port int) *stratum.Server {
	return stratum.NewServer(&stratum.ServerConfig{Host: "127.0.0.1", Port: port, MaxConnections: 4,
		ExtraNonce1Size: 4, ExtraNonce2Size: 8, MinDiff: 1, IsRentalPort: true}, zap.NewNop(), nil, nil)
}

// The dashboard tells the user to point a NiceHash or MRR order at the rental port only while
// the rental listener is up. Forge Solo for Windows starts without it when another program holds
// 3335, and the dashboard still advertised 3335: a paid order pointed there goes to that program.
func TestTheRentalPortIsAdvertisedOnlyWhileItListens(t *testing.T) {
	saved := stratumRentalServer
	t.Cleanup(func() { stratumRentalServer = saved })

	held, err := net.Listen("tcp", "127.0.0.1:0") // another program has the port
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	taken := rentalServerOn(held.Addr().(*net.TCPAddr).Port)
	if err := taken.Start(); err == nil {
		t.Fatal("RENTAL-PORT-SETUP: the rental port was not taken")
	}
	stratumRentalServer = taken
	if got := buildMiningStatus().RentalPort; got != 0 {
		t.Fatalf("RENTAL-PORT-TAKEN: with the rental port held by another program, the dashboard is told %d", got)
	}

	up := rentalServerOn(0)
	if err := up.Start(); err != nil {
		t.Fatal(err)
	}
	defer up.Stop()
	_, port, _ := net.SplitHostPort(up.ListenAddr())
	want, _ := strconv.Atoi(port)
	stratumRentalServer = up
	if got := buildMiningStatus().RentalPort; got != want {
		t.Fatalf("RENTAL-PORT-UP: the rental listener is up on %d, the dashboard is told %d", want, got)
	}

	stratumRentalServer = nil
	if got := buildMiningStatus().RentalPort; got != 0 {
		t.Fatalf("RENTAL-PORT-NONE: no rental listener, the dashboard is told %d", got)
	}
}
