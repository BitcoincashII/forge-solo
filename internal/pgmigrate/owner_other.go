//go:build !unix

package pgmigrate

// EnsureOwner does nothing on Windows: the launcher, the migrator and the programs all run as the
// signed-in user, who owns the files they make.
func EnsureOwner(db string, o *Owner) error { return nil }
