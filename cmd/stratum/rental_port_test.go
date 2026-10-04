package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/stratum"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func rentalConfigOn(host string, port int) *stratum.ServerConfig {
	return &stratum.ServerConfig{Host: host, Port: port, MaxConnections: 4,
		ExtraNonce1Size: 4, ExtraNonce2Size: 8, MinDiff: 1, IsRentalPort: true}
}

func rentalServerOn(port int) *stratum.Server {
	return stratum.NewServer(rentalConfigOn("127.0.0.1", port), zap.NewNop(), nil, nil)
}

// The dashboard tells the user to point a NiceHash or MRR order at the rental port only while
// the rental listener is up. Forge Solo for Windows starts without it when another program holds
// 3335, and the dashboard still advertised 3335: a paid order pointed there goes to that program.
// It is told which port another program holds, so that it can say so, and what to do; it sent
// orders to 3333 instead, whose difficulty floor an order arriving as one connection cannot use.
func TestTheRentalPortIsAdvertisedOnlyWhileItListens(t *testing.T) {
	saved, savedTaken, savedReserved, savedLogger := stratumRentalServer, rentalPortTaken, rentalPortReserved, logger
	t.Cleanup(func() {
		stratumRentalServer, rentalPortTaken, rentalPortReserved, logger = saved, savedTaken, savedReserved, savedLogger
	})
	logger = zap.NewNop()

	held, err := net.Listen("tcp", "127.0.0.1:0") // another program has the port
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	heldPort := held.Addr().(*net.TCPAddr).Port
	taken := rentalServerOn(heldPort)
	startRentalStratum(taken, rentalConfigOn("127.0.0.1", heldPort))
	stratumRentalServer = taken
	if got := buildMiningStatus().RentalPort; got != 0 {
		t.Fatalf("RENTAL-PORT-TAKEN: with the rental port held by another program, the dashboard is told %d", got)
	}
	if got := buildMiningStatus().RentalTaken; got != heldPort {
		t.Fatalf("RENTAL-PORT-TAKEN-SAID: another program holds the rental port %d, the dashboard is told %d", heldPort, got)
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
	if got := buildMiningStatus().RentalTaken; got != 0 {
		t.Fatalf("RENTAL-PORT-UP-NOT-TAKEN: the rental listener is up, the dashboard is told port %d is taken", got)
	}

	stratumRentalServer = nil
	if got := buildMiningStatus().RentalPort; got != 0 {
		t.Fatalf("RENTAL-PORT-NONE: no rental listener, the dashboard is told %d", got)
	}

	// A listener that fails for another reason is not said to be another program's.
	rentalPortTaken = 0
	bad := stratum.NewServer(rentalConfigOn("192.0.2.1", heldPort), zap.NewNop(), nil, nil) // an address this machine does not have
	startRentalStratum(bad, rentalConfigOn("192.0.2.1", heldPort))
	stratumRentalServer = bad
	if got := buildMiningStatus().RentalTaken; got != 0 {
		t.Fatalf("RENTAL-PORT-OTHER-FAILURE: a listener that could not have its address is said to be another program's port %d", got)
	}
	if got := buildMiningStatus().RentalReserved; got != 0 {
		t.Fatalf("RENTAL-PORT-OTHER-NOT-RESERVED: a listener that could not have its address is said to be on a port Windows keeps, %d", got)
	}
}

// listenError is a listen that failed with errno, as the stratum server returns it.
func listenError(errno syscall.Errno) error {
	return fmt.Errorf("failed to listen: %w", &net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", errno)})
}

// A rental port Windows keeps for itself (a range reserved for Hyper-V, WSL or Docker) is said to
// be Windows', in the launcher's words: the dashboard told the user to restart Forge Solo, which
// does not help while Windows keeps the port, and the tray and launcher.log said Windows keeps it.
func TestARentalPortWindowsKeepsIsSaidSo(t *testing.T) {
	saved, savedTaken, savedReserved, savedLogger := stratumRentalServer, rentalPortTaken, rentalPortReserved, logger
	t.Cleanup(func() {
		stratumRentalServer, rentalPortTaken, rentalPortReserved, logger = saved, savedTaken, savedReserved, savedLogger
	})
	core, logs := observer.New(zap.ErrorLevel)
	logger = zap.New(core)
	stratumRentalServer, rentalPortTaken, rentalPortReserved = nil, 0, 0
	rentalStratumFailed(3335, listenError(wsaeacces))
	st := buildMiningStatus()
	if st.RentalPort != 0 || st.RentalReserved != 3335 {
		t.Errorf("RENTAL-PORT-RESERVED-SAID: with Windows keeping the rental port, the dashboard is told port %d, kept %d", st.RentalPort, st.RentalReserved)
	}
	if st.RentalTaken != 0 {
		t.Errorf("RENTAL-PORT-RESERVED-NOT-TAKEN: a port Windows keeps is said to be another program's (%d)", st.RentalTaken)
	}
	want := "Windows keeps port 3335, the rental port, for itself: rentals have no port of their own until Windows lets it go and you restart Forge Solo"
	if logs.FilterMessage(want).Len() != 1 {
		t.Errorf("RENTAL-PORT-RESERVED-LOGGED: the mining service's log does not say %q: %v", want, logs.All())
	}

	up := rentalServerOn(0)
	if err := up.Start(); err != nil {
		t.Fatal(err)
	}
	defer up.Stop()
	stratumRentalServer = up
	if got := buildMiningStatus().RentalReserved; got != 0 {
		t.Fatalf("RENTAL-PORT-UP-NOT-RESERVED: the rental listener is up, the dashboard is told Windows keeps port %d", got)
	}
}

// A port another program holds, as each system says it: EADDRINUSE, and Windows' WSAEADDRINUSE.
// A port Windows keeps for itself: WSAEACCES, which the stratum's listen (exclusive address use)
// gets for nothing else.
func TestPortInUse(t *testing.T) {
	if !portInUse(listenError(syscall.EADDRINUSE)) || !portInUse(listenError(syscall.Errno(10048))) {
		t.Error("RENTAL-PORT-IN-USE: a port in use is not taken for one")
	}
	if portInUse(listenError(syscall.Errno(10013))) || portInUse(listenError(syscall.EADDRNOTAVAIL)) {
		t.Error("RENTAL-PORT-NOT-IN-USE: another failure is taken for a port in use")
	}
	if !portReserved(listenError(syscall.Errno(10013))) {
		t.Error("RENTAL-PORT-RESERVED: Windows' WSAEACCES is not taken for a port Windows keeps")
	}
	for _, errno := range []syscall.Errno{syscall.EADDRINUSE, syscall.Errno(10048), syscall.EACCES, syscall.EADDRNOTAVAIL} {
		if portReserved(listenError(errno)) {
			t.Errorf("RENTAL-PORT-NOT-RESERVED: %v is taken for a port Windows keeps", errno)
		}
	}
}
