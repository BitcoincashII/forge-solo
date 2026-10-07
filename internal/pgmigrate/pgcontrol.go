package pgmigrate

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io/fs"
	"os"
	"path/filepath"
)

// PostgreSQL 16's control file, global/pg_control, read without starting PostgreSQL: whether the
// cluster was shut down cleanly, and a hash of the whole file. PostgreSQL rewrites the file at every
// start and stop, so the hash taken at a move tells, later, whether 1.0.12 has run on the old data
// since.

// The PostgreSQL 16 layout (pg_control.h, little-endian as on every platform Forge Solo ships for).
const (
	ControlVersion16 = 1300 // pg_control_version of every PostgreSQL 16
	controlVersionAt = 8
	controlStateAt   = 16
	controlCRCAt     = 288 // offsetof(ControlFileData, crc): a CRC-32C of everything before it
)

// The cluster's state (DBState in pg_control.h). Only StateShutDown is a clean shutdown.
const (
	StateShutDown     = 1
	StateInProduction = 6
)

var (
	ErrControlMissing = errors.New("global/pg_control is missing")
	ErrControlDamaged = errors.New("global/pg_control is damaged")
)

// Control is what the control file says.
type Control struct {
	SystemIdentifier uint64
	Version          uint32
	State            uint32
	SHA256           string // of the whole file
}

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// ReadControl reads pgdata's control file. It returns ErrControlMissing when there is none, and
// ErrControlDamaged when it is not a PostgreSQL 16 control file whose checksum holds; SHA256 is set
// whenever the file could be read.
func ReadControl(pgdata string) (Control, error) {
	b, err := os.ReadFile(filepath.Join(pgdata, "global", "pg_control"))
	if errors.Is(err, fs.ErrNotExist) {
		return Control{}, ErrControlMissing
	}
	if err != nil {
		return Control{}, err
	}
	sum := sha256.Sum256(b)
	c := Control{SHA256: hex.EncodeToString(sum[:])}
	if len(b) < controlCRCAt+4 {
		return c, fmt.Errorf("%w: %d bytes", ErrControlDamaged, len(b))
	}
	le := binary.LittleEndian
	c.SystemIdentifier = le.Uint64(b)
	c.Version = le.Uint32(b[controlVersionAt:])
	c.State = le.Uint32(b[controlStateAt:])
	if c.Version != ControlVersion16 {
		return c, fmt.Errorf("%w: version %d, not PostgreSQL 16's %d", ErrControlDamaged, c.Version, ControlVersion16)
	}
	if crc32.Checksum(b[:controlCRCAt], castagnoli) != le.Uint32(b[controlCRCAt:]) {
		return c, fmt.Errorf("%w: its checksum does not match", ErrControlDamaged)
	}
	return c, nil
}
