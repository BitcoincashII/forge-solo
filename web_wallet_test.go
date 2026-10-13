package forgesolo

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The BCH2 Wallet, wallet.bch2.org, keeps its keys in the browser and refuses to be framed. Every
// page links it in a new tab, with no opener and no referrer, and nothing embeds another page.
var walletLink = regexp.MustCompile(`<a [^>]*href="https://wallet\.bch2\.org"[^>]*>`)

func checkWalletLinks(t *testing.T, code, where, src string, want int) {
	t.Helper()
	links := walletLink.FindAllString(src, -1)
	if len(links) != want {
		t.Errorf("%s: %s has %d links to the BCH2 Wallet, want %d", code, where, len(links), want)
	}
	for _, l := range links {
		if !strings.Contains(l, `target="_blank"`) || !strings.Contains(l, `rel="noopener noreferrer"`) {
			t.Errorf("%s: %s links the BCH2 Wallet without a new tab, noopener and noreferrer: %s", code, where, l)
		}
	}
}

// The dashboard's Wallet card sits under the hashrate tiles, which a first look at the dashboard
// keeps in view: above them, a card that is a third of a screen tall moved them below the fold. It is
// hidden until the API answers, says what it shows, how that differs from Matured at the top, and
// that Forge Solo cannot spend it, says where to spend the coins before it links the BCH2 Wallet,
// and links it once.
func TestWalletCardIsOnTheDashboard(t *testing.T) {
	solo := readWebFile(t, "solo.html")
	tiles, wallet, grid := strings.Index(solo, `<div class="stats-row"`), strings.Index(solo, `id="walletCard"`), strings.Index(solo, `<div class="main-grid">`)
	if tiles < 0 || wallet < tiles || grid < wallet {
		t.Fatalf("WEB-WALLET-CARD: the Wallet card is not right under the hashrate tiles (%d, %d, %d)", tiles, wallet, grid)
	}
	card := textBetween(solo, `<div class="card wallet-card" id="walletCard"`, `<div class="main-grid">`)
	for _, want := range []string{
		` hidden role="region" aria-labelledby="walletTitle">`,
		"The coins at your payout address now, as your node sees them: your block rewards, your TIDES payouts and anything else sent to it, less anything sent from it.",
		"Matured, at the top, adds up what your blocks paid, whether or not it is still there.",
		"Forge Solo reads them from its own node and holds no keys, so it cannot spend them.",
		`<div class="wl">Spendable</div>`, `<div class="wl">Still maturing</div>`,
		`<span id="walletAsOf"></span><span id="walletStatus" role="status"></span>`,
		`<span id="walletAuxAsOf"></span><span id="walletAuxStatus" role="status"></span>`,
		`<caption class="sr-only">`, "Each can be spent 100 blocks (for BCH2 about 17 hours) after the block that paid it.",
		"the BCH2 Wallet has no 1175</span>. The BCH2 Wallet keeps its keys in your browser, not on a server.</p>",
		`Open BCH2 Wallet <span aria-hidden="true">↗</span><span class="sr-only"> (opens in a new tab)</span></a>`,
	} {
		if !strings.Contains(card, want) {
			t.Errorf("WEB-WALLET-CARD: the card lacks %q", want)
		}
	}
	if spend, open := strings.Index(card, "Spend these coins from the wallet that holds your payout address's keys"), strings.Index(card, `<a class="wallet-btn"`); spend < 0 || open < spend {
		t.Error("WEB-WALLET-SPEND: the card does not say where to spend the coins before its link to the BCH2 Wallet")
	}
	checkWalletLinks(t, "WEB-WALLET-LINK", "the Wallet card", card, 1)

	// Nothing on any page frames another one: wallet.bch2.org would refuse, and nothing else should be.
	err := filepath.WalkDir("web/dist", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".html" && filepath.Ext(path) != ".js" || d.Name() == "chart.min.js" {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, tag := range []string{"<iframe", "<embed", "<object", "<frame"} {
			if strings.Contains(strings.ToLower(string(b)), tag) {
				t.Errorf("WEB-WALLET-NO-FRAME: %s has %s", path, tag)
			}
		}
		if strings.Contains(string(b), "wallet.bch2.org") {
			checkWalletLinks(t, "WEB-WALLET-LINK", path, string(b), strings.Count(string(b), `href="https://wallet.bch2.org"`))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The dashboard asks the API, which asks this install's own nodes, as often as it reads the stats,
// and writes what comes back as text: an address or a node's message never becomes markup.
func TestWalletCardReadsTheAPI(t *testing.T) {
	js := readWebFile(t, "js/pool-solo-inline.js")
	fetch := textBetween(js, "async function fetchWallet() {", "\n        }\n")
	render := textBetween(js, "function renderWalletCoin(prefix, w, unit, rows) {", "\n        }\n")
	if !strings.Contains(fetch, "apiFetch('/api/v1/wallet')") || !strings.Contains(js, "setInterval(fetchWallet, 30000);") {
		t.Errorf("WEB-WALLET-JS: the dashboard does not read /api/v1/wallet every 30 s:\n%s", fetch)
	}
	if strings.Contains(render, "innerHTML") || strings.Count(render, "setText(") != 6 {
		t.Errorf("WEB-WALLET-JS: the card's values are not all written as text:\n%s", render)
	}
	// The block number is apart from the status line, which a screen reader reads out whenever it
	// changes: it is not read out at every block. A spend from the address waits for a block too.
	if !strings.Contains(render, "setText(prefix + 'AsOf', ") || strings.Contains(textBetween(render, "setText(prefix + 'Status'", ");"), "f.height") ||
		!strings.Contains(render, "'Payments to or from this address show once a block includes them.'") {
		t.Errorf("WEB-WALLET-STATUS: %s", render)
	}
	set := textBetween(js, "function setText(id, text) {", "\n        }\n")
	if !strings.Contains(set, "el.textContent !== text") || !strings.Contains(set, "el.textContent = text") {
		t.Errorf("WEB-WALLET-JS: setText does not leave an unchanged status line alone:\n%s", set)
	}
	// The maturing table is the one piece of markup: its cells are numbers and fixed words.
	rows := textBetween(fetch, "document.getElementById('walletMaturingRows').innerHTML", ".join('');")
	for _, cell := range []string{"sanitizeHTML(r.unit === 'ESF' ? '1175' : 'BCH2')", "formatNumber(Number(r.m.height))",
		"sanitizeHTML(fmtCoins(r.m.sats, r.unit))", "formatNumber(Number(r.m.blocks_to_go))"} {
		if !strings.Contains(rows, cell) {
			t.Errorf("WEB-WALLET-JS: the maturing table does not write %s", cell)
		}
	}
	if strings.Contains(js, "fmtBCH2(") {
		t.Error("WEB-WALLET-JS: fmtBCH2 is still called; it is fmtCoins(sats, unit)")
	}
}

// While no payout address is set, Settings says where to get one: the BCH2 Wallet, which makes one in
// the browser. Forge Solo needs only the address, so the keys can stay elsewhere.
func TestSettingsSaysWhereToGetAnAddress(t *testing.T) {
	page := readWebFile(t, "settings.html")
	note := textBetween(page, `<p class="note" id="getAddressNote" hidden>`, "</p>")
	// The new tab is said inside the link, so a screen reader reads it as part of the link. The
	// link opens the wallet here; the keys stay elsewhere only when it is opened there.
	for _, want := range []string{"Need a BCH2 address?", `>Create one in the BCH2 Wallet<span class="sr-only"> (opens in a new tab)</span></a>`,
		"Forge Solo only needs the address, so the keys can stay on another device, such as your phone: open wallet.bch2.org there and copy its BCH2 address here."} {
		if !strings.Contains(note, want) {
			t.Errorf("WEB-SETTINGS-ADDRESS-HINT: the note lacks %q:\n%s", want, note)
		}
	}
	checkWalletLinks(t, "WEB-SETTINGS-ADDRESS-HINT", "the note", note, 1)
	if i, j := strings.Index(page, `id="poolAddr"`), strings.Index(page, `id="getAddressNote"`); i < 0 || j < i || j > strings.Index(page, `for="esfAddr"`) {
		t.Error("WEB-SETTINGS-ADDRESS-HINT: the note is not right under the payout address box")
	}
	apply := textBetween(page, "function applyConfig(d){", "\n   }\n")
	if !strings.Contains(apply, "document.getElementById('getAddressNote').hidden = !!pool;") ||
		strings.Index(apply, "getAddressNote") > strings.Index(apply, "return;") {
		t.Errorf("WEB-SETTINGS-ADDRESS-HINT: applyConfig does not show the note only without an address, in every mode:\n%s", apply)
	}
	failed := textBetween(page, ".catch(function(e){", "});")
	if !strings.Contains(failed, "document.getElementById('getAddressNote').hidden=true;") {
		t.Errorf("WEB-SETTINGS-ADDRESS-HINT: the note is not hidden when the settings cannot be read:\n%s", failed)
	}
}
