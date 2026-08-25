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
