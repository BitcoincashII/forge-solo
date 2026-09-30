// Package cashaddr reads and writes BCH2 CashAddr payout addresses without asking a node.
//
// Forge pays through the BCH2 node it runs on: v27.0.2 today, Landnám after the node swap.
// Landnám has no validateaddress, so the pool decodes addresses itself, and Decode accepts
// exactly the addresses BOTH nodes pay to:
//
//   - The checksum is taken under the chain prefix "bitcoincashii". An address with no prefix is
//     read under it; one naming another prefix is refused. v27.0.2 validateaddress refuses
//     bitcoincash: addresses (probed 2026-09-29), and Landnám's cashaddr.cpp Decode refuses
//     them too: "if (prefix != expected_prefix) return false;".
//   - One case throughout. Landnám: "Mixed case is refused rather than folded". v27.0.2 folds it.
//   - One spelling per address. Landnám refuses non-zero padding bits and a spare symbol ("Left-over
//     bits that are not zero were never written by an encoder"); v27.0.2 accepts both (probed).
//   - Type 0 (P2PKH) or 1 (P2SH), with a 20-byte hash. v27.0.2 validateaddress refuses the
//     token-aware types 2 and 3 and every other hash length (probed), and Landnám's
//     ScriptForAddress takes "TWENTY BYTES ONLY".
//   - No legacy base58. v27.0.2 accepts it, but Landnám has no base58 payment path.
package cashaddr

import (
	"errors"
	"fmt"
	"strings"
)

// MainnetPrefix is the BCH2 mainnet CashAddr prefix.
const MainnetPrefix = "bitcoincashii"

// Type is what an address pays to: the type field of the CashAddr version byte.
type Type byte

const (
	P2PKH Type = 0
	P2SH  Type = 1
)

// ErrInvalid is wrapped by every Decode refusal.
var ErrInvalid = errors.New("not a BCH2 payout address")

// maxLen bounds the input before any work is done: the longest CashAddr (a 64-byte hash) is 112
// symbols after its prefix.
const maxLen = 128

const charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

// Address is a decoded payout address.
type Address struct {
	Prefix string
	Type   Type
	Hash   [20]byte
}

// Decode reads s as an address of the chain named by prefix (lower case).
func Decode(s, prefix string) (Address, error) {
	if prefix == "" || len(s) > maxLen {
		return Address{}, fmt.Errorf("%w: bad length or prefix", ErrInvalid)
	}
	var lower, upper bool
	for i := 0; i < len(s); i++ {
		c := s[i]
		lower = lower || (c >= 'a' && c <= 'z')
		upper = upper || (c >= 'A' && c <= 'Z')
	}
	if lower && upper {
		return Address{}, fmt.Errorf("%w: mixed case", ErrInvalid)
	}
	body := strings.ToLower(s)
	if i := strings.IndexByte(body, ':'); i >= 0 {
		if body[:i] != prefix {
			return Address{}, fmt.Errorf("%w: prefix %q is not %q", ErrInvalid, body[:i], prefix)
		}
		body = body[i+1:]
	}
	if len(body) < 8 {
		return Address{}, fmt.Errorf("%w: too short", ErrInvalid)
	}
	values := make([]byte, len(body))
	for i := 0; i < len(body); i++ {
		v := strings.IndexByte(charset, body[i])
		if v < 0 {
			return Address{}, fmt.Errorf("%w: character %q", ErrInvalid, body[i])
		}
		values[i] = byte(v)
	}
	if polymod(append(expandPrefix(prefix), values...)) != 0 {
		return Address{}, fmt.Errorf("%w: checksum", ErrInvalid)
	}
	payload, ok := convertBits(values[:len(values)-8], 5, 8, false)
	if !ok || len(payload) == 0 {
		return Address{}, fmt.Errorf("%w: padding", ErrInvalid)
	}
	version := payload[0]
	if version&0x80 != 0 {
		return Address{}, fmt.Errorf("%w: reserved version bit", ErrInvalid)
	}
	if version&0x07 != 0 || len(payload) != 21 {
		return Address{}, fmt.Errorf("%w: hash is not 20 bytes", ErrInvalid)
	}
	t := Type(version >> 3 & 0x0f)
	if t != P2PKH && t != P2SH {
		return Address{}, fmt.Errorf("%w: type %d", ErrInvalid, t)
	}
	a := Address{Prefix: prefix, Type: t}
	copy(a.Hash[:], payload[1:])
	return a, nil
}

// Encode writes the canonical (lower-case, prefixed) form of an address.
func Encode(prefix string, t Type, hash [20]byte) string {
	values, _ := convertBits(append([]byte{byte(t) << 3}, hash[:]...), 8, 5, true)
	m := polymod(append(append(expandPrefix(prefix), values...), make([]byte, 8)...))
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteByte(':')
	for _, v := range values {
		b.WriteByte(charset[v])
	}
	for i := 0; i < 8; i++ {
		b.WriteByte(charset[(m>>uint(5*(7-i)))&31])
	}
	return b.String()
}

// String is the canonical form of a.
func (a Address) String() string { return Encode(a.Prefix, a.Type, a.Hash) }

// Script is the output script a pays to: OP_DUP OP_HASH160 <hash> OP_EQUALVERIFY OP_CHECKSIG for
// P2PKH, OP_HASH160 <hash> OP_EQUAL for P2SH.
func (a Address) Script() []byte {
	if a.Type == P2SH {
		return append(append([]byte{0xa9, 0x14}, a.Hash[:]...), 0x87)
	}
	return append(append([]byte{0x76, 0xa9, 0x14}, a.Hash[:]...), 0x88, 0xac)
}

func expandPrefix(prefix string) []byte {
	out := make([]byte, 0, len(prefix)+1)
	for i := 0; i < len(prefix); i++ {
		out = append(out, prefix[i]&0x1f)
	}
	return append(out, 0)
}

func polymod(values []byte) uint64 {
	c := uint64(1)
	for _, d := range values {
		c0 := c >> 35
		c = (c&0x07ffffffff)<<5 ^ uint64(d)
		if c0&0x01 != 0 {
			c ^= 0x98f2bc8e61
		}
		if c0&0x02 != 0 {
			c ^= 0x79b76d99e2
		}
		if c0&0x04 != 0 {
			c ^= 0xf33e5fb3c4
		}
		if c0&0x08 != 0 {
			c ^= 0xae2eabe2a8
		}
		if c0&0x10 != 0 {
			c ^= 0x1e4f43e470
		}
	}
	return c ^ 1
}

// convertBits regroups from-bit values into to-bit values. Decoding (pad false) refuses a spare
// group and non-zero padding bits, so every address has exactly one spelling.
func convertBits(in []byte, from, to uint, pad bool) ([]byte, bool) {
	var acc, bits uint
	maxv := uint(1)<<to - 1
	out := make([]byte, 0, len(in)*int(from)/int(to)+1)
	for _, v := range in {
		if uint(v)>>from != 0 {
			return nil, false
		}
		acc = acc<<from | uint(v)
		bits += from
		for bits >= to {
			bits -= to
			out = append(out, byte(acc>>bits&maxv))
		}
	}
	if pad {
		if bits > 0 {
			out = append(out, byte(acc<<(to-bits)&maxv))
		}
	} else if bits >= from || acc<<(to-bits)&maxv != 0 {
		return nil, false
	}
	return out, true
}
