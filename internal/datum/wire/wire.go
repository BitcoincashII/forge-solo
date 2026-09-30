// Package wire is what a DATUM gateway (Forge Solo in pool mode) and the pool agree on: the API
// messages, the payout every DATUM coinbase must make, the coinbase layout, and how requests are
// signed. It depends only on internal/tides and internal/cashaddr, so the gateway can carry an
// identical copy.
//
// DATUM here is Forge's own take on OCEAN's idea (ocean.xyz/docs/datum): each gateway builds its
// own block templates from its own node, and every DATUM coinbase pays the pool's TIDES split
// directly -- the pool never holds the reward.
package wire

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/BitcoincashII/forge-solo/internal/cashaddr"
	"github.com/BitcoincashII/forge-solo/internal/tides"
)

// ExtranonceSize is the extranonce reserved in every DATUM coinbase scriptSig: a 4-byte
// extranonce1 (the gateway's session) and an 8-byte extranonce2 (the miner's), as on the pool.
const ExtranonceSize = 12

// MaxTag bounds the gateway's coinbase tag; with the height push and the extranonce the scriptSig
// stays well under consensus's 100 bytes.
const MaxTag = 32

// MaxShareDiffExp bounds a committed share difficulty's exponent either way: 2^-64 to 2^64.
const MaxShareDiffExp = 64

// Snapshot is the pool's TIDES state at one moment. Every DATUM coinbase built on it pays
// Payouts(snapshot, value, finder).
type Snapshot struct {
	Version     int64              `json:"version"`
	Height      int64              `json:"height"`   // the height a template on this snapshot mines
	PrevHash    string             `json:"prevhash"` // RPC byte order
	NetworkDiff float64            `json:"network_difficulty"`
	WindowWork  float64            `json:"window_work"`
	Work        map[string]float64 `json:"work"`  // miner address -> work in the share-log window
	Carry       map[string]int64   `json:"carry"` // miner address -> satoshis owed below dust
	Dust        int64              `json:"dust"`
	At          time.Time          `json:"at"`
}

// Payouts is the coinbase a template on snap must pay for a coinbase worth value: the TIDES split
// of value over the window with no pool fee, or all of it to finder while the window is empty.
func Payouts(snap *Snapshot, value int64, finder string) ([]tides.Output, error) {
	if snap == nil {
		return nil, errors.New("no snapshot")
	}
	outs, _, err := tides.Split(value, snap.Work, snap.Carry, 0, "", snap.Dust)
	if errors.Is(err, tides.ErrNoWork) {
		if _, derr := cashaddr.Decode(finder, cashaddr.MainnetPrefix); derr != nil {
			return nil, fmt.Errorf("finder %q: %w", finder, derr)
		}
		return []tides.Output{{Address: finder, Sats: value}}, nil
	}
	return outs, err
}

// JobRequest registers a gateway's block template with the pool.
type JobRequest struct {
	Snapshot int64             `json:"snapshot"` // the snapshot version the coinbase pays
	Height   int64             `json:"height"`
	PrevHash string            `json:"prevhash"` // RPC byte order
	Version  uint32            `json:"version"`  // the template's block version, before rolling
	Bits     string            `json:"bits"`
	Time     uint32            `json:"time"`             // the template's curtime
	Coinb1   string            `json:"coinb1"`           // coinbase up to the extranonce (hex)
	Coinb2   string            `json:"coinb2"`           // coinbase after the extranonce (hex)
	Finder   string            `json:"finder"`           // paid in full while the share log is empty
	TxIDs    []string          `json:"txids"`            // the template's other transactions, block order
	TxData   map[string]string `json:"txdata,omitempty"` // raw hex of txids the pool may not know yet
	// ShareDiffExp, when set, commits the job to a share difficulty of at least 2^ShareDiffExp.
	// The coinbase tag's last byte is this exponent (CommitShareDiff), so the difficulty is part
	// of what every share on the job hashes and cannot be chosen once a hash is known. The pool
	// credits each share on the job at least that difficulty, so a gateway whose miners work at a
	// high difficulty is credited for it: the way OCEAN's DATUM Gateway puts each miner's
	// difficulty into its coinbase. Unset: the pool's own share difficulty only.
	ShareDiffExp *int `json:"share_diff_exp,omitempty"`
}

// CommitShareDiff returns tag with the share difficulty exponent exp as its last byte, cut to fit
// MaxTag. A JobRequest's ShareDiffExp tells the pool to check that byte.
func CommitShareDiff(tag []byte, exp int) []byte {
	if len(tag) > MaxTag-1 {
		tag = tag[:MaxTag-1]
	}
	return append(append([]byte(nil), tag...), byte(int8(exp)))
}

// CommittedShareDiff is the share difficulty req commits its coinbase cb to, or 0 when req commits
// to none; an error when req says it commits and cb's tag does not.
func CommittedShareDiff(req *JobRequest, cb *Coinbase) (float64, error) {
	if req.ShareDiffExp == nil {
		return 0, nil
	}
	e := *req.ShareDiffExp
	if e < -MaxShareDiffExp || e > MaxShareDiffExp {
		return 0, fmt.Errorf("share difficulty exponent %d is outside ±%d", e, MaxShareDiffExp)
	}
	if len(cb.Tag) == 0 || cb.Tag[len(cb.Tag)-1] != byte(int8(e)) {
		return 0, fmt.Errorf("the coinbase tag does not end with share difficulty exponent %d", e)
	}
	return math.Ldexp(1, e), nil
}

// JobResponse answers a registration.
type JobResponse struct {
	JobID           string  `json:"job_id,omitempty"`
	ShareDifficulty float64 `json:"share_difficulty,omitempty"`
	Error           string  `json:"error,omitempty"`
	// Retry: the pool is catching up (a new tip, or a transaction it just took); register the same
	// template again shortly.
	Retry bool `json:"retry,omitempty"`
}

// Share is one piece of work a gateway's miner found on a registered job, at or above the job's
// share difficulty. Its fields are those of a stratum mining.submit.
type Share struct {
	JobID      string `json:"job_id"`
	Miner      string `json:"miner"` // the address credited in the share log
	Worker     string `json:"worker,omitempty"`
	Extranonce string `json:"extranonce"` // ExtranonceSize bytes, hex: extranonce1 + extranonce2
	NTime      string `json:"ntime"`
	Nonce      string `json:"nonce"`
	Version    string `json:"version,omitempty"` // rolled version, as mining.submit sends it
}

// ShareBatch carries up to MaxBatch shares.
type ShareBatch struct {
	Shares []Share `json:"shares"`
}

const MaxBatch = 500

// ShareResult is the pool's verdict on one share, in batch order.
type ShareResult struct {
	Accepted bool   `json:"accepted"`
	Block    bool   `json:"block,omitempty"` // the share is a block; the pool submitted it too
	Error    string `json:"error,omitempty"`
}

type ShareBatchResponse struct {
	Results         []ShareResult `json:"results"`
	ShareDifficulty float64       `json:"share_difficulty"` // for jobs registered from now on
	Error           string        `json:"error,omitempty"`
}

// ---- coinbase ---------------------------------------------------------------------------------

// HeightPush is the BIP34 height push that opens a coinbase scriptSig: the height as a minimal
// little-endian script number, the same bytes the pool's own coinbase writes.
func HeightPush(height int64) []byte {
	if height <= 0 {
		return []byte{0x01, 0x00}
	}
	var b []byte
	for h := height; h > 0; h >>= 8 {
		b = append(b, byte(h&0xff))
	}
	if b[len(b)-1] >= 0x80 {
		b = append(b, 0x00)
	}
	return append([]byte{byte(len(b))}, b...)
}

// BuildCoinbase lays out a DATUM coinbase: one input with the BIP34 height push, the extranonce and
// the tag in its scriptSig, then the outputs. It returns the halves either side of the extranonce.
func BuildCoinbase(height int64, tag []byte, outs []tides.Output) (string, string, error) {
	heightPush := HeightPush(height)
	if len(tag) > MaxTag {
		return "", "", fmt.Errorf("coinbase tag is %d bytes, at most %d", len(tag), MaxTag)
	}
	scriptLen := len(heightPush) + ExtranonceSize + len(tag)
	var c1, c2 bytes.Buffer
	binary.Write(&c1, binary.LittleEndian, uint32(1)) // version
	c1.WriteByte(1)                                   // one input
	c1.Write(make([]byte, 32))                        // null prevout
	binary.Write(&c1, binary.LittleEndian, uint32(0xffffffff))
	c1.WriteByte(byte(scriptLen))
	c1.Write(heightPush)

	c2.Write(tag)
	binary.Write(&c2, binary.LittleEndian, uint32(0xffffffff)) // sequence
	writeVarInt(&c2, uint64(len(outs)))
	for _, o := range outs {
		a, err := cashaddr.Decode(o.Address, cashaddr.MainnetPrefix)
		if err != nil {
			return "", "", fmt.Errorf("output %q: %w", o.Address, err)
		}
		if o.Sats <= 0 {
			return "", "", fmt.Errorf("output %q: %d satoshis", o.Address, o.Sats)
		}
		binary.Write(&c2, binary.LittleEndian, uint64(o.Sats))
		script := a.Script()
		writeVarInt(&c2, uint64(len(script)))
		c2.Write(script)
	}
	binary.Write(&c2, binary.LittleEndian, uint32(0)) // locktime
	return hex.EncodeToString(c1.Bytes()), hex.EncodeToString(c2.Bytes()), nil
}

// TxOut is one parsed coinbase output.
type TxOut struct {
	Value  int64
	Script []byte
}

// Coinbase is a parsed DATUM coinbase.
type Coinbase struct {
	Height  int64 // from the BIP34 push, which must be minimally encoded
	Tag     []byte
	Outputs []TxOut
	Size    int // serialized size with the extranonce in place
}

// ParseCoinbase checks that coinb1 + extranonce + coinb2 is a coinbase in the DATUM layout and
// returns its parts. The layout is strict -- exactly what BuildCoinbase writes -- so pool and
// gateway can never disagree about where the extranonce is.
func ParseCoinbase(coinb1, coinb2 string) (*Coinbase, error) {
	b1, err := hex.DecodeString(coinb1)
	if err != nil {
		return nil, fmt.Errorf("coinb1: %w", err)
	}
	b2, err := hex.DecodeString(coinb2)
	if err != nil {
		return nil, fmt.Errorf("coinb2: %w", err)
	}
	const head = 4 + 1 + 32 + 4 + 1 // version, input count, null prevout, scriptSig length
	if len(b1) < head+1 || len(b1) > head+9 {
		return nil, fmt.Errorf("coinb1 is %d bytes", len(b1))
	}
	if binary.LittleEndian.Uint32(b1) != 1 || b1[4] != 1 || !bytes.Equal(b1[5:37], make([]byte, 32)) ||
		binary.LittleEndian.Uint32(b1[37:41]) != 0xffffffff {
		return nil, errors.New("coinb1 is not a version-1 coinbase with one null input")
	}
	scriptLen := int(b1[41])
	push := b1[42:]
	if len(push) < 2 || int(push[0]) != len(push)-1 {
		return nil, errors.New("coinb1 does not end with the BIP34 height push")
	}
	tagLen := scriptLen - len(push) - ExtranonceSize
	if tagLen < 0 || tagLen > MaxTag || scriptLen > 100 {
		return nil, fmt.Errorf("scriptSig length %d leaves a %d-byte tag", scriptLen, tagLen)
	}
	var height int64
	for i := len(push) - 1; i >= 1; i-- {
		height = height<<8 | int64(push[i])
	}
	if len(push) > 5 || push[len(push)-1]&0x80 != 0 || !bytes.Equal(push, HeightPush(height)) {
		return nil, errors.New("the height push is not a minimal positive height")
	}
	r := bytes.NewReader(b2)
	cb := &Coinbase{Height: height, Tag: make([]byte, tagLen)}
	if _, err := io.ReadFull(r, cb.Tag); err != nil {
		return nil, errors.New("coinb2 is shorter than the tag")
	}
	var seq uint32
	if err := binary.Read(r, binary.LittleEndian, &seq); err != nil || seq != 0xffffffff {
		return nil, errors.New("coinb2: bad sequence")
	}
	n, err := readVarInt(r)
	if err != nil || n == 0 || n > 10_000 {
		return nil, fmt.Errorf("coinb2: %d outputs", n)
	}
	for i := uint64(0); i < n; i++ {
		var v uint64
		if err := binary.Read(r, binary.LittleEndian, &v); err != nil || v > 21_000_000*100_000_000 {
			return nil, fmt.Errorf("output %d: bad value", i)
		}
		sl, err := readVarInt(r)
		if err != nil || sl > 10_000 {
			return nil, fmt.Errorf("output %d: bad script length", i)
		}
		script := make([]byte, sl)
		if _, err := io.ReadFull(r, script); err != nil {
			return nil, fmt.Errorf("output %d: short script", i)
		}
		cb.Outputs = append(cb.Outputs, TxOut{Value: int64(v), Script: script})
	}
	var lock uint32
	if err := binary.Read(r, binary.LittleEndian, &lock); err != nil || lock != 0 || r.Len() != 0 {
		return nil, errors.New("coinb2: bad locktime or trailing bytes")
	}
	cb.Size = len(b1) + ExtranonceSize + len(b2)
	return cb, nil
}

// Value is the coinbase's total output value.
func (c *Coinbase) Value() int64 {
	var v int64
	for _, o := range c.Outputs {
		v += o.Value
	}
	return v
}

// PaysExactly checks that the coinbase's outputs are want, in order, script for script.
func (c *Coinbase) PaysExactly(want []tides.Output) error {
	if len(c.Outputs) != len(want) {
		return fmt.Errorf("coinbase has %d outputs, the split has %d", len(c.Outputs), len(want))
	}
	for i, w := range want {
		a, err := cashaddr.Decode(w.Address, cashaddr.MainnetPrefix)
		if err != nil {
			return fmt.Errorf("split output %q: %w", w.Address, err)
		}
		if c.Outputs[i].Value != w.Sats || !bytes.Equal(c.Outputs[i].Script, a.Script()) {
			return fmt.Errorf("output %d pays %d to %x, the split pays %d to %s", i, c.Outputs[i].Value, c.Outputs[i].Script, w.Sats, w.Address)
		}
	}
	return nil
}

func writeVarInt(b *bytes.Buffer, n uint64) {
	switch {
	case n < 0xfd:
		b.WriteByte(byte(n))
	case n <= 0xffff:
		b.WriteByte(0xfd)
		binary.Write(b, binary.LittleEndian, uint16(n))
	case n <= 0xffffffff:
		b.WriteByte(0xfe)
		binary.Write(b, binary.LittleEndian, uint32(n))
	default:
		b.WriteByte(0xff)
		binary.Write(b, binary.LittleEndian, n)
	}
}

// WriteVarInt appends a Bitcoin compact-size integer.
func WriteVarInt(b *bytes.Buffer, n uint64) { writeVarInt(b, n) }

func readVarInt(r *bytes.Reader) (uint64, error) {
	p, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	switch p {
	case 0xfd:
		var v uint16
		err = binary.Read(r, binary.LittleEndian, &v)
		return uint64(v), err
	case 0xfe:
		var v uint32
		err = binary.Read(r, binary.LittleEndian, &v)
		return uint64(v), err
	case 0xff:
		var v uint64
		err = binary.Read(r, binary.LittleEndian, &v)
		return v, err
	default:
		return uint64(p), nil
	}
}

// ---- request signing --------------------------------------------------------------------------

// A gateway is its Ed25519 key: it makes one on first run and signs every request with it. The
// pool keeps no secret and needs no sign-up; it can rate-limit and block by key.
const (
	HeaderKey  = "X-Datum-Key"
	HeaderTime = "X-Datum-Time"
	HeaderSig  = "X-Datum-Sig"
	// MaxClockSkew bounds how old or how far ahead a signed request may be.
	MaxClockSkew = 2 * time.Minute
)

func signedMessage(method, path string, unix int64, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte("forge-datum-v1\n" + method + "\n" + path + "\n" + strconv.FormatInt(unix, 10) + "\n" + hex.EncodeToString(sum[:]))
}

// Sign adds the signature headers to req, whose body is body.
func Sign(req *http.Request, priv ed25519.PrivateKey, body []byte, now time.Time) {
	unix := now.Unix()
	req.Header.Set(HeaderKey, hex.EncodeToString(priv.Public().(ed25519.PublicKey)))
	req.Header.Set(HeaderTime, strconv.FormatInt(unix, 10))
	req.Header.Set(HeaderSig, hex.EncodeToString(ed25519.Sign(priv, signedMessage(req.Method, req.URL.Path, unix, body))))
}

// Verify checks req's signature over body and returns the gateway's key (hex).
func Verify(req *http.Request, body []byte, now time.Time) (string, error) {
	keyHex := req.Header.Get(HeaderKey)
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return "", errors.New("bad or missing gateway key")
	}
	unix, err := strconv.ParseInt(req.Header.Get(HeaderTime), 10, 64)
	if err != nil {
		return "", errors.New("bad or missing request time")
	}
	if d := now.Sub(time.Unix(unix, 0)); d > MaxClockSkew || d < -MaxClockSkew {
		return "", fmt.Errorf("request time is %s off the pool's clock", d.Round(time.Second))
	}
	sig, err := hex.DecodeString(req.Header.Get(HeaderSig))
	if err != nil || !ed25519.Verify(key, signedMessage(req.Method, req.URL.Path, unix, body), sig) {
		return "", errors.New("bad signature")
	}
	return keyHex, nil
}
