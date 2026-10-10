package mining

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/BitcoincashII/forge-solo/internal/cashaddr"
)

func addrFor(b byte) (string, []byte) {
	var h [20]byte
	for i := range h {
		h[i] = b
	}
	return cashaddr.Encode(cashaddr.MainnetPrefix, cashaddr.P2PKH, h), h[:]
}

// A job pays, and says it pays, the address in effect when it was built: after a change on the
// dashboard the next job pays the new address, and the one before it still says the old one.
func TestAJobSaysWhichAddressItPays(t *testing.T) {
	jm := NewJobManager("", "", "", "", "")
	if got := jm.PayoutAddress(); got != "" {
		t.Fatalf("PAY3-UNSET: no address set, yet jobs would pay %q", got)
	}
	a, ha := addrFor(1)
	b, hb := addrFor(2)
	tmpl := &BlockTemplate{Version: 0x20000000, PreviousBlockHash: strings.Repeat("00", 32), Bits: "1902c9b9",
		Height: 83470, CurTime: 1_700_000_000, CoinbaseValue: 50_0000_0000}
	pays := func(j *Job, h []byte) bool {
		return strings.Contains(j.CoinBase2, "76a914"+hex.EncodeToString(h)+"88ac")
	}

	if err := jm.SetPoolAddress(a); err != nil {
		t.Fatal(err)
	}
	old := jm.CreateJob(tmpl)
	if err := jm.SetPoolAddress(b); err != nil {
		t.Fatal(err)
	}
	cur := jm.CreateJob(tmpl)
	if old.PayTo != a || !pays(old, ha) {
		t.Fatalf("PAY3-JOB-PAYTO: a job built for %s says it pays %q (pays it: %v)", a, old.PayTo, pays(old, ha))
	}
	if cur.PayTo != b || !pays(cur, hb) || jm.PayoutAddress() != b {
		t.Fatalf("PAY3-JOB-PAYTO-NEW: after the change the job says %q, pays the new address: %v, manager says %q", cur.PayTo, pays(cur, hb), jm.PayoutAddress())
	}
}

// The address the stratum starts with (POOL_ADDRESS) is the one its first jobs say they pay.
func TestTheStartingAddressIsTheOneJobsPay(t *testing.T) {
	a, ha := addrFor(3)
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"result":{"isvalid":true,"scriptPubKey":"76a914%s88ac"},"error":null}`, hex.EncodeToString(ha))
	}))
	defer node.Close()
	jm := NewJobManager(node.URL, "u", "p", a, "")
	tmpl := &BlockTemplate{Version: 0x20000000, PreviousBlockHash: strings.Repeat("00", 32), Bits: "1902c9b9",
		Height: 83470, CurTime: 1_700_000_000, CoinbaseValue: 50_0000_0000}
	if got, job := jm.PayoutAddress(), jm.CreateJob(tmpl); got != a || job == nil || job.PayTo != a {
		t.Fatalf("PAY3-START: started with %s, the manager says %q and its first job %+v", a, got, job)
	}
}

// What the job manager says goes to the standard library's log, not to stdout. Forge Gateway keeps
// that log in its own, at debug; the payout address's pubkey hash, printed to stdout at every
// settings apply, went raw into forge-gateway.log, which the Windows tray app fills from the
// gateway's stdout.
func TestTheJobManagerSaysNothingOnStdout(t *testing.T) {
	a, ha := addrFor(4)
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"result":{"isvalid":true,"scriptPubKey":"76a914%s88ac"},"error":null}`, hex.EncodeToString(ha))
	}))
	defer node.Close()
	var logged bytes.Buffer
	defer log.SetOutput(log.Writer())
	log.SetOutput(&logged)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	setErr := NewJobManager(node.URL, "u", "p", "", "").SetPoolAddress(a)
	os.Stdout = stdout
	w.Close()
	printed, _ := io.ReadAll(r)
	if setErr != nil {
		t.Fatal(setErr)
	}
	if len(printed) != 0 {
		t.Errorf("PAY3-STDOUT: the job manager printed on stdout: %q", printed)
	}
	if want := "Payout address pubkey hash (from node): " + hex.EncodeToString(ha); !strings.Contains(logged.String(), want) {
		t.Errorf("PAY3-STDOUT-LOG: the log does not say %q:\n%s", want, logged.String())
	}
}
