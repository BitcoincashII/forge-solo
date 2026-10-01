package forgesolo

// No raw 64-bit sync/atomic call may take the address of a struct field.
//
// Forge Solo for Linux ships 32-bit builds (armv6l, armv7l, i686). There a 64-bit atomic
// operation panics ("unaligned 64-bit atomic operation") unless its word is 8-byte aligned, and
// Go guarantees that only for the first word of an allocation and for the sync/atomic types
// (atomic.Int64, atomic.Uint64). A struct field reached through atomic.AddInt64(&s.field, …) is
// aligned or not by accident of layout: the 32-bit stratum crashed on its first miner that way.
// Make such a field an atomic.Int64 or atomic.Uint64 instead.
//
// A source check, so it fails on the 64-bit machines most tests run on. CI also runs the whole
// suite as GOARCH=386, which catches whatever this pattern does not.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestNoRaw64BitAtomicsOnStructFields(t *testing.T) {
	raw := regexp.MustCompile(`atomic\.(Add|Load|Store|Swap|CompareAndSwap)(Int64|Uint64)\(&[A-Za-z_][A-Za-z0-9_]*(\.|\[)`)
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (strings.HasPrefix(d.Name(), ".") && path != "." || d.Name() == "dist") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if raw.MatchString(line) {
				t.Errorf("%s:%d: a raw 64-bit atomic on a struct field panics on 32-bit platforms; "+
					"make the field an atomic.Int64 or atomic.Uint64:\n\t%s", path, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
