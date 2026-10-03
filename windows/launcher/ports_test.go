package main

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

// holdWindow finds portWindow+1 free ports in a row from `from` on, listens on the first
// portWindow of them and leaves the last one free. It returns the window's start and its listeners.
func holdWindow(t *testing.T, from int) (int, []net.Listener) {
	t.Helper()
	for ; from+portWindow < 29000; from += portWindow + 1 {
		var held []net.Listener
		for p := from; p <= from+portWindow; p++ {
			l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
			if err != nil {
				break
			}
			held = append(held, l)
		}
		if len(held) == portWindow+1 {
			_ = held[portWindow].Close()
			held = held[:portWindow]
			t.Cleanup(func() {
				for _, l := range held {
					_ = l.Close()
				}
			})
			return from, held
		}
		for _, l := range held {
			_ = l.Close()
		}
	}
	t.Fatalf("no %d free ports in a row below 29000", portWindow+1)
	return 0, nil
}

// A service is never given a port another program holds. With every port of its window taken
// there is none for it, though the port just past the window is free. A port freed in the
// window is used, the first one when there are two.
func TestPickPortNeverFallsBackToATakenPort(t *testing.T) {
	from, held := holdWindow(t, 20000)
	if p, err := pickPort(from); err == nil {
		t.Fatalf("PORT-FAIL-CLOSED: %d-%d are all taken, yet pickPort gave %s", from, from+portWindow-1, p)
	}
	last := from + portWindow - 1
	_ = held[portWindow-1].Close()
	if p, err := pickPort(from); err != nil || p != strconv.Itoa(last) {
		t.Fatalf("PORT-LAST: got %q (%v), want %d, the window's last port and the only free one", p, err, last)
	}
	_ = held[100].Close()
	if p, err := pickPort(from); err != nil || p != strconv.Itoa(from+100) {
		t.Fatalf("PORT-FIRST-FREE: got %q (%v), want %d", p, err, from+100)
	}
}

// Nothing may start without every loopback port: one that cannot be had fails the launch, and the
// error names the service.
func TestAssignPortsNeedsEveryPort(t *testing.T) {
	full, held := holdWindow(t, 20000)
	free, freeHeld := holdWindow(t, full+portWindow+1)
	for _, l := range freeHeld {
		_ = l.Close()
	}
	var a, b string
	saved := portPlan
	t.Cleanup(func() { portPlan = saved })
	portPlan = []portSlot{{"the first", &a, free}, {"the database", &b, full}}

	err := assignPorts()
	if err == nil {
		t.Fatalf("PORTS-ALL-OR-NOTHING: the database's window %d-%d is full, yet the ports were assigned (%s)", full, full+portWindow-1, b)
	}
	if !strings.Contains(err.Error(), "the database") {
		t.Errorf("PORTS-NAMED: %q does not say which service has no port", err)
	}
	_ = held[0].Close()
	if err := assignPorts(); err != nil || a != strconv.Itoa(free) || b != strconv.Itoa(full) {
		t.Fatalf("PORTS-ASSIGNED: %v, ports %q %q, want %d %d", err, a, b, free, full)
	}
}

// Every loopback port is picked, each from its own window. No two windows overlap, none holds a
// fixed port, and all stay below 49152, where the ports Windows hands out for outbound
// connections start.
func TestPortPlan(t *testing.T) {
	want := map[*string]bool{&pgPort: true, &bch2RPC: true, &bch2ZMQ: true, &aux1175RPC: true, &stratumInt: true, &apiPort: true}
	for _, s := range portPlan {
		delete(want, s.port)
	}
	if len(want) != 0 || len(portPlan) != 6 {
		t.Errorf("PORT-PLAN-COMPLETE: %d of the six loopback ports are not in the plan (%d entries)", len(want), len(portPlan))
	}
	fixed := []string{minerPort, rentalPort, webPort, bch2P2P, aux1175P2P, "8340"}
	for i, a := range portPlan {
		if a.from+portWindow > 49152 {
			t.Errorf("PORT-EPHEMERAL: %s reaches %d", a.name, a.from+portWindow-1)
		}
		for _, b := range portPlan[i+1:] {
			if a.from < b.from+portWindow && b.from < a.from+portWindow {
				t.Errorf("PORT-WINDOWS-OVERLAP: %s (%d) and %s (%d)", a.name, a.from, b.name, b.from)
			}
		}
		for _, f := range fixed {
			if n, _ := strconv.Atoi(f); n >= a.from && n < a.from+portWindow {
				t.Errorf("PORT-FIXED: %s's window holds the fixed port %s", a.name, f)
			}
		}
	}
}
