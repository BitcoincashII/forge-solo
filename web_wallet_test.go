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

// The dashboard's Wallet card sits under the balance card and above the TIDES card, so in TIDES mode
// it follows "Your TIDES payouts are not counted here". It is hidden until the API answers, says
// what it shows and that Forge Solo cannot spend it, and links the BCH2 Wallet once.
func TestWalletCardIsOnTheDashboard(t *testing.T) {
	solo := readWebFile(t, "solo.html")
	balance, wallet, tides := strings.Index(solo, `<div class="balance-card"`), strings.Index(solo, `id="walletCard"`), strings.Index(solo, `id="tidesCard"`)
	if balance < 0 || wallet < balance || tides < wallet {
		t.Fatalf("WEB-WALLET-CARD: the Wallet card is not between the balance card and the TIDES card (%d, %d, %d)", balance, wallet, tides)
	}
	card := textBetween(solo, `<div class="card wallet-card" id="walletCard"`, "<!-- TIDES mode:")
	for _, want := range []string{
		` hidden role="region" aria-labelledby="walletTitle">`,
		"The coins at your payout address, as your node sees them: your block rewards, your TIDES payouts and anything else sent to it.",
		"Forge Solo reads them from its own node and holds no keys, so it cannot spend them.",
		`<div class="wl">Spendable</div>`, `<div class="wl">Still maturing</div>`,
		`id="walletStatus" role="status"`, `id="walletAuxStatus" role="status"`,
		`<caption class="sr-only">`, "Each can be spent 100 blocks after the block that paid it.",
		`Open BCH2 Wallet <span aria-hidden="true">↗</span><span class="sr-only"> (opens in a new tab)</span></a>`,
		"The BCH2 Wallet keeps its keys in your browser, not on a server.",
	} {
		if !strings.Contains(card, want) {
			t.Errorf("WEB-WALLET-CARD: the card lacks %q", want)
		}
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
	if strings.Contains(render, "innerHTML") || strings.Count(render, "setText(") != 5 {
		t.Errorf("WEB-WALLET-JS: the card's values are not all written as text:\n%s", render)
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
	for _, want := range []string{"Need a BCH2 address?", ">Create one in the BCH2 Wallet</a>",
		"Forge Solo only needs the address, so the keys can stay on another device, such as your phone."} {
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
