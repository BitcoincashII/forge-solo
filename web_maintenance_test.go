package forgesolo

// After a failed move of an earlier version's data into the new database the API runs in
// maintenance mode (cmd/api/maintenance.go): its health answer says status "maintenance", every
// other read answers 503, and the stratum does not mine. The pages must say so instead of showing
// a dashboard that cannot load, and Settings must offer the way back once the old data was left
// out. There is no browser in CI, so the wiring is pinned here as text.

import (
	"regexp"
	"strings"
	"testing"
)

// emDash is the dash the dashboard's own text never uses.
const emDash = rune(0x2014)

func TestDashboardShowsTheFailedMove(t *testing.T) {
	js := readWebFile(t, "js/common.js")
	old := textBetween(js, "const OldData = {", "\n};\n")
	if old == "" {
		t.Fatal("WEB-MAINT-MISSING: common.js has no OldData")
	}
	ready := textBetween(js, "document.addEventListener('DOMContentLoaded', () => {", "});")
	if !regexp.MustCompile(`(?m)^\s*OldData\.init\(\);`).MatchString(ready) {
		t.Error("WEB-MAINT-INIT: the pages never ask whether the move failed")
	}
	if !strings.Contains(old, "fetch('/api/v1/health', { cache: 'no-store' })") {
		t.Error("WEB-MAINT-HEALTH: OldData does not read the health answer")
	}
	init := textBetween(old, "init() {", "\n    },")
	if !strings.Contains(init, "if (h.status === 'maintenance') {\n                this.showNotice(h);") {
		t.Errorf("WEB-MAINT-NOTICE: maintenance does not bring up the notice: %s", init)
	}
	if !strings.Contains(init, "} else if (h.migration && this.banner[h.migration.state]) {\n                this.showBanner(h.migration);") {
		t.Errorf("WEB-MAINT-BANNER: the move's state does not bring up the banner: %s", init)
	}

	// What the notice says, the same on Umbrel and Windows but for where the password is.
	notice := textBetween(old, "showNotice(h) {", "\n    },")
	for _, want := range []string{"Forge Solo could not move its data", "${sanitizeHTML(h.reason || 'see the log')}",
		"<strong>Nothing was lost.</strong>", "Forge Solo does not mine until this is settled; the nodes keep running.",
		"<strong>Restart Forge Solo to try again.</strong>", "Settings can bring it in later",
		"set it again in Settings after the restart", `id="maintenancePw" type="text" autocomplete="off"`,
		"this.setChosen(h.skip_file === true, '');", "this.choose(!this.chosen)",
		"setInterval(() => this.read().then(d => { if (d && d.status !== 'maintenance') location.reload(); }), 10000);"} {
		if !strings.Contains(notice, want) {
			t.Errorf("WEB-MAINT-TEXT: the notice lacks %q", want)
		}
	}
	if !strings.Contains(old, "chosen ? 'Try the move again instead' : 'Start without the old data'") {
		t.Error("WEB-MAINT-BUTTON: the notice has no 'Start without the old data' button")
	}
	if strings.Count(js, "h.platform") != 1 || !strings.Contains(notice, "this.pwWhere[h.platform] || this.pwWhere.umbrel") {
		t.Error("WEB-MAINT-SAME: the notice differs by platform beyond where the password is")
	}
	where := textBetween(old, "pwWhere: {", "\n    },")
	for _, want := range []string{"umbrel: 'It is the Default password umbrelOS shows for Forge Solo", "Default credentials",
		"It is not your Umbrel login password.", "windows: 'Right-click the Forge Solo icon", "Copy Settings Password"} {
		if !strings.Contains(where, want) {
			t.Errorf("WEB-MAINT-PW-WHERE: the notice does not say where the password is: lacks %q", want)
		}
	}

	// The choice goes to the API behind the settings password, as every change.
	choose := textBetween(old, "choose(skip) {", "\n    }\n")
	for _, want := range []string{"if (pw) headers['X-Forge-Password'] = pw;", "'Content-Type': 'application/json'",
		"fetch('/api/v1/old-data', { method: 'POST', headers: headers, body: JSON.stringify({ skip: skip }) })",
		"this.setChosen(skip, d.message);", "} else if (d && d.password_required) {", "Nothing was changed."} {
		if !strings.Contains(choose, want) {
			t.Errorf("WEB-MAINT-POST: choosing lacks %q", want)
		}
	}

	// The banners, one per state the API runs normally in.
	banner := textBetween(old, "banner: {", "\n    },")
	for state, want := range map[string]string{
		"deferred": "The move finishes at the next restart of Forge Solo.",
		"degraded": "is damaged or partly deleted and is ignored",
		"skipped":  "Forge Solo started without the data of its earlier version, as chosen.",
		"failed":   "Restart Forge Solo to try again.",
	} {
		if !regexp.MustCompile(`(?m)^\s+` + state + `: '[^'\n]*` + regexp.QuoteMeta(want)).MatchString(banner) {
			t.Errorf("WEB-MAINT-BANNERS: no %s banner saying %q", state, want)
		}
	}
	if strings.ContainsRune(old, emDash) {
		t.Error("WEB-MAINT-EMDASH: OldData's text has an em-dash")
	}

	found := map[string]bool{}
	for _, r := range cssRules(readWebFile(t, "css/common.css")) {
		switch {
		case hasSelector(r, ".maintenance-notice"):
			found[".maintenance-notice"] = declares(r.body, "position", "fixed") && declares(r.body, "inset", "0") && strings.Contains(r.body, "z-index")
		case hasSelector(r, ".migration-banner"):
			found[".migration-banner"] = true
		case hasSelector(r, "#maintenancePw"):
			found["#maintenancePw"] = strings.Contains(r.body, "-webkit-text-security:disc")
		}
	}
	for _, sel := range []string{".maintenance-notice", ".migration-banner", "#maintenancePw"} {
		if !found[sel] {
			t.Errorf("WEB-MAINT-CSS: common.css has no working %s rule (a notice over the whole page, the banner, a masked password box)", sel)
		}
	}
}

func TestSettingsCanBringTheOldDataIn(t *testing.T) {
	s := readWebFile(t, "settings.html")
	card := regexp.MustCompile(`(?s)<div class="card" id="oldDataCard" style="display:none">(.*?)</div>\n  </div>`).FindStringSubmatch(s)
	if card == nil {
		t.Fatal("WEB-OLDDATA-CARD: Settings has no hidden 'Data from before 1.0.13' card")
	}
	for _, want := range []string{"<h2>Data from before 1.0.13</h2>", `id="oldDataText"`, `id="oldDataBtn"`, `id="oldDataStatus"`} {
		if !strings.Contains(card[1], want) {
			t.Errorf("WEB-OLDDATA-CARD: the card lacks %s", want)
		}
	}
	script := textBetween(s, "var OLD_DATA_TEXT={", "\n })();")
	for _, want := range []string{
		"readJSON('/api/v1/health').then(function(h){ showOldData(h.migration); })",
		"if(!m || m.state!=='skipped'){ card.style.display='none'; return; }",
		"var bringIn=m.skip_file===true;",
		"b.textContent=bringIn ? 'Bring the old data in' : 'Keep starting without it';",
		"fetch('/api/v1/old-data',{method:'POST',headers:headers,body:JSON.stringify({skip:skip})})",
		"headers['X-Forge-Password']=pw;",
		"jsonOrNull(r)",
		"} else if(d.password_required){",
		"Nothing was changed.",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("WEB-OLDDATA-SCRIPT: Settings lacks %q", want)
		}
	}
	if strings.ContainsRune(script, emDash) || strings.ContainsRune(card[1], emDash) {
		t.Error("WEB-OLDDATA-EMDASH: the card's text has an em-dash")
	}
}
