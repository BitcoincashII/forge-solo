package forgesolo

// This app must never contain a wallet-send path.
//
// Every reward is paid on-chain by the block's own coinbase, directly to the configured
// address. There is no operator wallet: the BCH2 node in docker-compose.yml is started with
// no -wallet flag at all, so the /wallet/main%2Fpool RPC endpoint the old sender targeted
// does not exist.
//
// That sender was not dead code. Installs upgraded from v1.0.0 carry payout rows with a
// NULL txid (v1.0.0 recorded solo blocks via SavePayoutAtomicWithSolo;
// SaveSoloBlockCoinbaseDirect only arrived in v1.0.6, with no backfill), and
// ReserveMaturePayouts selects exactly those rows. The only thing standing between them and
// a doomed 60-second sendtoaddress retry loop was the minimum-payout threshold — so
// removing that threshold on its own would have WIDENED what was attempted, not narrowed
// it. The sender was removed instead.
//
// A source check, because this is about absence: nothing can fail at runtime to tell us the
// path came back.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestNoWalletSendPathExists(t *testing.T) {
	banned := map[string]string{
		"sendtoaddress": "the wallet-send RPC; this app has no wallet to send from",
		"payMiner(":     "the sender; its row source ReserveMaturePayouts selects NULL-txid rows",
		"walletRPCURL":  "the /wallet/... RPC endpoint, which the shipped node does not create",
	}
	for _, path := range []string{"cmd/stratum/main.go", "cmd/api/main.go"} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		// Strip comments: the surrounding prose deliberately explains why these are gone,
		// and a guard that trips on its own explanation is a guard nobody keeps.
		var b strings.Builder
		for _, line := range strings.Split(string(src), "\n") {
			if t := strings.TrimSpace(line); strings.HasPrefix(t, "//") {
				continue
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
		body := b.String()
		for needle, why := range banned {
			if strings.Contains(body, needle) {
				t.Errorf("%s contains %q — %s. If a payout sender is genuinely wanted, it "+
					"needs a wallet on the node, a threshold, and a decision about the "+
					"legacy NULL-txid rows; it must not reappear by accident.",
					path, needle, why)
			}
		}
	}
}

// The 1175 side has no send path either.
//
// A pool-style reserve->send->finalize pipeline used to sit in internal/stats/payout1175.go
// behind a comment warning that wiring it in would DOUBLE-PAY on top of the aux coinbase.
// No shipped version ever called it -- v1.0.0, v1.0.6 and v1.0.8 all have zero call sites --
// so it was deleted. A comment cannot stop a future edit; an absent function can.
func TestNo1175SendPipeline(t *testing.T) {
	src, err := os.ReadFile("internal/stats/payout1175.go")
	if err != nil {
		t.Fatalf("read payout1175.go: %v", err)
	}
	body := string(src)
	for _, fn := range []string{
		"func Process1175PayoutAtomic(", "func Finalize1175Payout(",
		"func Revert1175PayoutMark(", "func StuckSending1175(",
	} {
		if strings.Contains(body, fn) {
			t.Errorf("payout1175.go declares %s — 1175 is aux-coinbase-direct, so a secondary "+
				"send pays the miner twice. Any 1175 send path needs a design that answers "+
				"how it avoids that, not a revival of this one.", fn)
		}
	}
	// status='sending' is only reachable through that pipeline; nothing may write it.
	if strings.Contains(body, "SET status='sending'") {
		t.Error("payout1175.go can write status='sending' again; the sweep that reconciled " +
			"those rows was removed because nothing could produce them")
	}
}

// And the minimum-payout concept must not come back with it.
func TestNoMinimumPayoutConcept(t *testing.T) {
	for _, path := range []string{
		"cmd/stratum/main.go", "docker/stratum/config.template.yaml",
		"docker-compose.yml", ".env.example",
	} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, needle := range []string{"min_payout", "MIN_PAYOUT", "minPayout"} {
			if strings.Contains(string(src), needle) {
				t.Errorf("%s still references %q. A solo block pays its finder in its own "+
					"coinbase; there is no balance to accumulate and no threshold to cross.",
					path, needle)
			}
		}
	}
}

// Test fixtures must use obviously-fake addresses.
//
// A real regtest payout address was once pasted into a test straight from a running
// harness, and it landed in the public repo. It held nothing and its key is long gone, but
// a real-format address in test data is indistinguishable at a glance from one that
// matters — and the reader cannot tell which without checking a chain.
//
// The convention is a self-describing stub padded with zeroes: qtestminer000…, qorphan000…,
// qreject000…. Two exceptions are deliberate and allow-listed below: CashAddr checksum
// vectors, which must be genuine to test the checksum at all.
func TestTestAddressesAreObviouslyFake(t *testing.T) {
	allowed := map[string]string{
		// Real by necessity: these exercise the CashAddr checksum itself.
		"bitcoincashii:qpgf7a4mx6hpsjd3qnl9pr7ee09j7rk4zclycv4c8m": "valid checksum vector",
		"bitcoincashii:qpgf7a4mx6hpsjd3qnl9pr7ee09j7rk4zclycv4c8n": "one-char-off checksum vector",
		"bitcoincashii:qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzse6qye33q": "canonical all-zero-payload vector",
		"bitcoincashii:qzeh9rcyyy8jlyalgh84e8fst6xh649hly2tfwgvwc": "checksum-valid ledger fixture",
		"bitcoincashii:qpvg5aehqc3mtrmf2say7tmn0t9cxcw0wsahs5d9r5": "checksum-valid 1175 fixture",
	}
	re := regexp.MustCompile(`bitcoincashii:q[a-z0-9]{10,}`)

	var walk func(dir string) error
	walk = func(dir string) error {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range ents {
			full := filepath.Join(dir, e.Name())
			if e.IsDir() {
				if e.Name() == ".git" || e.Name() == "node_modules" {
					continue
				}
				if err := walk(full); err != nil {
					return err
				}
				continue
			}
			if !strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			src, err := os.ReadFile(full)
			if err != nil {
				return err
			}
			for _, m := range re.FindAllString(string(src), -1) {
				if _, ok := allowed[m]; ok {
					continue
				}
				// A fake is a short stub, or padded with a run of zeroes.
				body := strings.TrimPrefix(m, "bitcoincashii:q")
				if len(body) < 12 || strings.Contains(body, "0000") {
					continue
				}
				t.Errorf("%s uses %s — that looks like a real address. Test fixtures should "+
					"be self-describing stubs (qtestminer000…). If it must be checksum-valid, "+
					"add it to the allow-list here with the reason.", full, m)
			}
		}
		return nil
	}
	if err := walk("."); err != nil {
		t.Fatalf("walk: %v", err)
	}
}
