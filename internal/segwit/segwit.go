// Package segwit reads segwit addresses (BIP 173, BIP 350) without asking a node: the 1175 (ESF)
// payout address, which the 1175 node's getauxblock pays.
//
// Settings, the api's PAYOUT_ADDRESS_1175 fallback, the Wallet card and the stratum all take a 1175
// address through Script, so an address one of them takes is one the others take, and one the 1175
// node takes. 1.0.12 to 1.0.15 checked only the bech32 checksum and the "esf" prefix: an address
// with a 19-byte witness program was saved, the 1175 node refused every getauxblock for it ("-5
// Invalid payout address"), and merge-mining never worked. Script takes exactly what the 1175
// node's validateaddress and getauxblock take: scripts/it-1175.sh puts every kind of address to a
// v29.1.0 regtest node (node_it_test.go), and a synced mainnet node agreed on all 312 esf1… ones
// when this was written.
package segwit

import (
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/btcutil/bech32"
)

// HRP1175 is the human-readable part of a 1175 mainnet address (esf1…). Its regtest one is "resf".
const HRP1175 = "esf"

// ErrInvalid is wrapped by every Script refusal.
var ErrInvalid = errors.New("not a segwit address")

// Script is the output script of the segwit address addr of the chain whose human-readable part is
// hrp: witness version 0 with a bech32 checksum and a 20- or 32-byte program, or version 1 to 16
// with a bech32m checksum and 2 to 40 bytes. One case throughout (either), at most 90 characters,
// and no padding bits but zeros, as BIP 173 has it.
func Script(addr, hrp string) ([]byte, error) {
	got, data, checksum, err := bech32.DecodeGeneric(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if got != hrp {
		return nil, fmt.Errorf("%w: it is for %s1…, not %s1…", ErrInvalid, got, hrp)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: it has no witness version", ErrInvalid)
	}
	version := data[0]
	program, err := bech32.ConvertBits(data[1:], 5, 8, false)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	switch {
	case version > 16:
		return nil, fmt.Errorf("%w: witness version %d", ErrInvalid, version)
	case len(program) < 2 || len(program) > 40:
		return nil, fmt.Errorf("%w: a witness program of %d bytes", ErrInvalid, len(program))
	case version == 0 && len(program) != 20 && len(program) != 32:
		return nil, fmt.Errorf("%w: a version 0 witness program of %d bytes, not 20 or 32", ErrInvalid, len(program))
	case version == 0 && checksum != bech32.Version0:
		return nil, fmt.Errorf("%w: witness version 0 takes a bech32 checksum, not bech32m", ErrInvalid)
	case version > 0 && checksum != bech32.VersionM:
		return nil, fmt.Errorf("%w: witness version %d takes a bech32m checksum, not bech32", ErrInvalid, version)
	}
	op := byte(0) // OP_0, or OP_1 to OP_16
	if version > 0 {
		op = 0x50 + version
	}
	return append([]byte{op, byte(len(program))}, program...), nil
}

// Check1175 says why addr is not a 1175 mainnet payout address, or nil when it is one.
func Check1175(addr string) error {
	_, err := Script(addr, HRP1175)
	return err
}
