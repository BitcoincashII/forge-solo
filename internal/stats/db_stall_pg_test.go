//go:build !sqlite

package stats

import (
	"net"
	"strings"
	"testing"
	"time"
)

// A database server that takes connections and never answers: connecting, and so IsDBConnected,
// gives up instead of holding the caller.
func TestAStalledDatabaseDoesNotHoldItsCallers(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		var held []net.Conn
		defer func() {
			for _, c := range held {
				c.Close()
			}
		}()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			held = append(held, c) // says nothing
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	t.Setenv("DB_HOST", host)
	t.Setenv("DB_PORT", port)
	saved := dbConnectTimeout
	dbConnectTimeout = 1
	t.Cleanup(func() { dbConnectTimeout = saved })
	conn := GetDBConnStr()
	if !strings.Contains(conn, "connect_timeout=1") {
		t.Fatalf("DATA10-CONNSTR: %q has no connect_timeout", conn)
	}
	start := time.Now()
	if err := InitDB(conn); err == nil {
		CloseDB()
		t.Fatal("DATA10-CONNECT: a server that never answered was taken for a database")
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("DATA10-CONNECT: connecting to a stalled server took %s", el)
	}
}
