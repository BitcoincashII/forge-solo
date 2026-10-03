package stats

import (
	"database/sql"
	"errors"
)

// Payout modes. Solo is the default and what every install ran before TIDES existed: blocks pay
// the miner's own address in full. TIDES makes this install a DATUM gateway to Forge Pool: its
// blocks pay the pool's TIDES split, and its miners are paid from every DATUM block, whoever
// finds it.
const (
	PayoutModeSolo  = "solo"
	PayoutModeTides = "tides"
)

// ValidPayoutMode reports whether mode is one the app knows.
func ValidPayoutMode(mode string) bool {
	return mode == PayoutModeSolo || mode == PayoutModeTides
}

// GetPayoutMode returns the dashboard-chosen payout mode; solo when nothing was ever chosen or
// the stored value is not one the app knows.
//
// Portable SQL, shared by both backends like payout1175.go.
func GetPayoutMode() (string, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return PayoutModeSolo, ErrDatabaseNotInitialized
	}
	var mode string
	err := db.QueryRow(`SELECT COALESCE(payout_mode, '') FROM pool_config WHERE id = 1`).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return PayoutModeSolo, nil
	}
	if err != nil {
		return PayoutModeSolo, err
	}
	if !ValidPayoutMode(mode) {
		return PayoutModeSolo, nil
	}
	return mode, nil
}

// SavePoolSettings stores what the Settings page saves -- the payout address, the 1175 address,
// the coinbase tag and, unless mode is empty, the payout mode -- in one write, so a save is all
// or nothing. Written as two, a failure between them left the addresses and tag saved while the
// page said nothing was.
func SavePoolSettings(poolAddr, payout1175, tag, mode string) error {
	if mode == "" {
		return SavePoolConfig(poolAddr, payout1175, tag)
	}
	if !ValidPayoutMode(mode) {
		return errors.New("payout mode must be solo or tides")
	}
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized
	}
	_, err := db.Exec(`
		INSERT INTO pool_config (id, pool_address, payout_address_1175, coinbase_tag, payout_mode, updated_at)
		VALUES (1, $1, $2, $3, $4, CURRENT_TIMESTAMP)
		ON CONFLICT (id) DO UPDATE
		SET pool_address = EXCLUDED.pool_address,
		    payout_address_1175 = EXCLUDED.payout_address_1175,
		    coinbase_tag = EXCLUDED.coinbase_tag,
		    payout_mode = EXCLUDED.payout_mode,
		    updated_at = CURRENT_TIMESTAMP`, poolAddr, payout1175, tag, mode)
	return err
}

// SavePayoutMode stores the payout mode, leaving the rest of pool_config as it is.
func SavePayoutMode(mode string) error {
	if !ValidPayoutMode(mode) {
		return errors.New("payout mode must be solo or tides")
	}
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return ErrDatabaseNotInitialized
	}
	_, err := db.Exec(`
		INSERT INTO pool_config (id, payout_mode, updated_at)
		VALUES (1, $1, CURRENT_TIMESTAMP)
		ON CONFLICT (id) DO UPDATE
		SET payout_mode = EXCLUDED.payout_mode,
		    updated_at = CURRENT_TIMESTAMP`, mode)
	return err
}

// GatewaySeed returns this install's TIDES gateway key seed (hex), storing fresh the first time
// it is asked for. The first stored seed wins, so two processes asking at once agree on one
// identity. The key only names the gateway at Forge Pool (rate limits, blocking); losing it just
// means starting over as a new gateway.
func GatewaySeed(fresh string) (string, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if db == nil {
		return "", ErrDatabaseNotInitialized
	}
	if _, err := db.Exec(`INSERT INTO datum_identity (id, key_seed) VALUES (1, $1) ON CONFLICT (id) DO NOTHING`, fresh); err != nil {
		return "", err
	}
	var seed string
	if err := db.QueryRow(`SELECT key_seed FROM datum_identity WHERE id = 1`).Scan(&seed); err != nil {
		return "", err
	}
	return seed, nil
}
