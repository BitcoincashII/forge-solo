package mining

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/bch2/forge-pool/internal/mergemining"
)

// TestMergeMineMultiTxParent_Live drives the REAL pool code — CreateJob (coinbase
// commitment + multi-tx merkle branch) and mergemining.AssembleAuxPowHex (the same
// assembler the stratum share path uses) — against a live 1175 regtest node, and
// confirms a solved MULTI-transaction parent block is accepted via submitauxblock
// and advances the aux chain. This is the end-to-end merge-mining proof.
//
// Gated on MM_REGTEST_RPC (+ _USER/_PASS/_PAYOUT), set by the runner script
// scratchpad/mm_regtest_e2e.sh which starts the node past AuxPoW activation.
func TestMergeMineMultiTxParent_Live(t *testing.T) {
	rpcURL := os.Getenv("MM_REGTEST_RPC")
	if rpcURL == "" {
		t.Skip("MM_REGTEST_RPC not set; skipping live merge-mining integration test")
	}
	user, pass, payout := os.Getenv("MM_REGTEST_USER"), os.Getenv("MM_REGTEST_PASS"), os.Getenv("MM_REGTEST_PAYOUT")

	// Real job manager with merge mining enabled against the live aux node.
	jm := &JobManager{pubkeyHash: make([]byte, 20)}
	jm.EnableMergeMining(rpcURL, user, pass, payout)

	// A parent template with MULTIPLE transactions so CreateJob builds a non-trivial
	// coinbase merkle branch. The txids are arbitrary: the aux node validates only
	// coinbase-branch consistency, not the parent's other transactions.
	var txs []TxData
	for i := 0; i < 4; i++ {
		var h [32]byte
		for j := range h {
			h[j] = byte(i*31 + j + 1)
		}
		txs = append(txs, TxData{TxID: hex.EncodeToString(h[:]), Data: "00"})
	}
	template := &BlockTemplate{
		Version:           0x20000000,
		PreviousBlockHash: "00000000000000000000000000000000000000000000000000000000000000ff",
		Transactions:      txs,
		CoinbaseValue:     5000000000,
		Bits:              "207fffff",
		Height:            250,
		CurTime:           1000000,
		Target:            "7fffff0000000000000000000000000000000000000000000000000000000000",
	}

	job := jm.CreateJob(template)
	if job.AuxWork == nil {
		t.Fatal("CreateJob returned no AuxWork — is the aux node past activation and reachable?")
	}
	if len(job.MerkleBranches) == 0 {
		t.Fatal("expected a non-empty coinbase merkle branch for a multi-tx parent")
	}
	t.Logf("aux work: height=%d hash=%s branches=%d", job.AuxWork.Height, job.AuxWork.Hash, len(job.MerkleBranches))

	// Simulate a miner's winning share: reconstruct the coinbase, fold the merkle
	// root through the branch, build the 80-byte parent header, and grind the nonce
	// until the parent hash meets the aux target (~2 tries at regtest difficulty).
	en1, en2 := "010203040506", "00000001"
	coinbase := reconstructCoinbase(job.CoinBase1, en1, en2, job.CoinBase2)
	merkleRoot := foldMerkle(coinbase, job.MerkleBranches)

	target, ok := new(big.Int).SetString(job.AuxWork.Target, 16)
	if !ok {
		t.Fatalf("bad aux target %q", job.AuxWork.Target)
	}
	var header []byte
	var nonce uint32
	for nonce = 0; nonce < 5_000_000; nonce++ {
		header = buildParentHeader(0x20000000, merkleRoot, 1000000, 0x207fffff, nonce)
		if new(big.Int).SetBytes(rev(doubleSHA256(header))).Cmp(target) <= 0 {
			break
		}
	}
	t.Logf("solved parent nonce=%d", nonce)

	// Assemble via the SAME function the stratum share path uses.
	auxHex, err := mergemining.AssembleAuxPowHex(coinbase, header, job.MerkleBranches)
	if err != nil {
		t.Fatal(err)
	}

	before := regtestBlockCount(t, rpcURL, user, pass)
	accepted, err := mergemining.NewClient(rpcURL, user, pass).SubmitAuxBlock(job.AuxWork.Hash, auxHex)
	if err != nil {
		t.Fatalf("submitauxblock error: %v", err)
	}
	if !accepted {
		t.Fatalf("aux node REJECTED the multi-tx AuxPoW (branches=%d)", len(job.MerkleBranches))
	}
	time.Sleep(500 * time.Millisecond) // let the node connect the block
	after := regtestBlockCount(t, rpcURL, user, pass)
	if after != before+1 {
		t.Fatalf("aux height did not advance: before=%d after=%d", before, after)
	}
	t.Logf("✅ multi-tx AuxPoW accepted; aux chain %d -> %d", before, after)
}

func reconstructCoinbase(cb1, en1, en2, cb2 string) []byte {
	var b bytes.Buffer
	for _, s := range []string{cb1, en1, en2, cb2} {
		x, _ := hex.DecodeString(s)
		b.Write(x)
	}
	return b.Bytes()
}

func foldMerkle(coinbase []byte, branchesHex []string) []byte {
	root := doubleSHA256(coinbase)
	for _, h := range branchesHex {
		b, _ := hex.DecodeString(h)
		root = doubleSHA256(append(append([]byte{}, root...), b...))
	}
	return root
}

func buildParentHeader(version uint32, merkleRoot []byte, ntime, bits, nonce uint32) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, version)
	b.Write(make([]byte, 32)) // prevhash (not validated for the aux parent)
	b.Write(merkleRoot)       // 32B internal little-endian
	binary.Write(&b, binary.LittleEndian, ntime)
	binary.Write(&b, binary.LittleEndian, bits)
	binary.Write(&b, binary.LittleEndian, nonce)
	return b.Bytes()
}

func regtestBlockCount(t *testing.T, url, user, pass string) int {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "1.0", "id": "t", "method": "getblockcount", "params": []any{}})
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.SetBasicAuth(user, pass)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var r struct {
		Result int `json:"result"`
	}
	json.NewDecoder(resp.Body).Decode(&r)
	return r.Result
}
