//go:build !sqlite

// Command forge-solo-migrate writes forgesolo.db, which only the build with -tags sqlite can: this
// build says so and stops.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "forge-solo-migrate was built without -tags sqlite, so it cannot write forgesolo.db")
	os.Exit(2)
}
