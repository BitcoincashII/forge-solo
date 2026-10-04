package main

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// fakeHost stands in for the machine install-service works on. It records what install does, in
// order, and fails the steps named in fail.
type fakeHost struct {
	unitText  string // the installed unit; "" for none
	running   bool
	left      []int // the optional ports another program holds
	fail      map[string]error
	calls     []string
	unitWrote string
}

func (h *fakeHost) step(name string) error {
	h.calls = append(h.calls, name)
	return h.fail[name]
}

func (h *fakeHost) unit() (string, bool) { return h.unitText, h.unitText != "" }
func (h *fakeHost) active() bool         { return h.running }
func (h *fakeHost) systemctl(args ...string) error {
	err := h.step("systemctl " + strings.Join(args, " "))
	if err == nil && len(args) > 0 {
		switch args[0] {
		case "stop":
			h.running = false
		case "start", "restart":
			h.running = true
		}
	}
	return err
}
func (h *fakeHost) checkPorts(web string) ([]int, error) { return h.left, h.step("ports " + web) }
func (h *fakeHost) checkWeb(web string) error            { return h.step("web " + web) }
func (h *fakeHost) copyRelease() error                   { return h.step("copy") }
func (h *fakeHost) ensureUser() error                    { return h.step("user") }
func (h *fakeHost) ensureData() error                    { return h.step("data") }
func (h *fakeHost) writeUnit(text string) error {
	h.unitWrote = text
	return h.step("unit")
}
func (h *fakeHost) waitServing(web string) error { return h.step("serving " + web) }
func (h *fakeHost) journalTail()                 { h.calls = append(h.calls, "journal") }
func (h *fakeHost) firewallHint(string)          {}

func (h *fakeHost) did(call string) bool {
	for _, c := range h.calls {
		if c == call {
			return true
		}
	}
	return false
}

// An upgrade keeps the dashboard address the installed service has, and says so: it used to move a
// dashboard served to the network back to 127.0.0.1, and the network lost it.
func TestInstallKeepsTheDashboardAddress(t *testing.T) {
	h := &fakeHost{unitText: unitFile("0.0.0.0:3080"), running: true}
	var out strings.Builder
	if err := install(h, &out, defaultWeb, false); err != nil {
		t.Fatal(err)
	}
	if unitWeb(h.unitWrote) != "0.0.0.0:3080" || !h.did("serving 0.0.0.0:3080") {
		t.Errorf("WEB-KEPT: an upgrade without --web gave the service %q (calls %q)", unitWeb(h.unitWrote), h.calls)
	}
	if s := out.String(); !strings.Contains(s, "0.0.0.0:3080, kept from the installed service") {
		t.Errorf("WEB-KEPT-SAID: the output does not say the address was kept:\n%s", s)
	}

	// --web given: that address, and the change is named. 127.0.0.1 moves it back to this machine.
	h = &fakeHost{unitText: unitFile("0.0.0.0:3080"), running: true}
	out.Reset()
	if err := install(h, &out, "127.0.0.1:3080", true); err != nil {
		t.Fatal(err)
	}
	if unitWeb(h.unitWrote) != "127.0.0.1:3080" {
		t.Errorf("WEB-GIVEN: --web 127.0.0.1:3080 gave the service %q", unitWeb(h.unitWrote))
	}
	if !strings.Contains(out.String(), "127.0.0.1:3080, changed from 0.0.0.0:3080") {
		t.Errorf("WEB-CHANGED-SAID: the output does not name the change:\n%s", out.String())
	}

	// A first install: the default, and nothing to say about it.
	h = &fakeHost{}
	out.Reset()
	if err := install(h, &out, defaultWeb, false); err != nil {
		t.Fatal(err)
	}
	if unitWeb(h.unitWrote) != defaultWeb || strings.Contains(out.String(), "kept") || strings.Contains(out.String(), "changed") {
		t.Errorf("WEB-FIRST: a first install gave %q and said:\n%s", unitWeb(h.unitWrote), out.String())
	}
}

func TestUnitWeb(t *testing.T) {
	for unit, want := range map[string]string{
		unitFile("0.0.0.0:3080"): "0.0.0.0:3080",
		unitFile("[::]:3090"):    "[::]:3090",
		"[Service]\nExecStart=/opt/forge-solo/forge-solo run --web=192.168.1.5:80 --data-dir /x\n": "192.168.1.5:80",
		"[Service]\nExecStart=/opt/forge-solo/forge-solo run --data-dir /var/lib/forge-solo\n":     "",
		"[Service]\nExecStart=/opt/forge-solo/forge-solo run --web\n":                              "",
	} {
		if got := unitWeb(unit); got != want {
			t.Errorf("UNIT-WEB: %q gave %q, want %q", unit, got, want)
		}
	}
	// One that is not an address gives way to the default, and is named.
	if web, note := chooseWeb(defaultWeb, false, "nonsense"); web != defaultWeb || !strings.Contains(note, "nonsense") {
		t.Errorf("UNIT-WEB-BAD: %q %q", web, note)
	}
}

// Another program on the rental port: the service is installed without it, and says so.
func TestInstallWithoutTheRentalPort(t *testing.T) {
	var out strings.Builder
	if err := install(&fakeHost{}, &out, defaultWeb, false); err != nil || !strings.Contains(out.String(), "(rentals: 3335)") {
		t.Errorf("INSTALL-RENTALS: %v\n%s", err, out.String())
	}
	out.Reset()
	if err := install(&fakeHost{left: []int{rentalPort}}, &out, defaultWeb, false); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "Note: "+leftOutNote(rentalPort)) || !strings.Contains(s, "(no rentals: another program has port 3335)") {
		t.Errorf("INSTALL-NO-RENTALS: the output does not say the rental port is left out:\n%s", s)
	}
}

var errTest = errors.New("test failure")

// What can fail is checked while the service still runs: install-service used to stop it first,
// then fail on another program's port, and leave it stopped.
func TestInstallChecksBeforeStopping(t *testing.T) {
	// A new dashboard port another program holds.
	h := &fakeHost{unitText: unitFile(defaultWeb), running: true, fail: map[string]error{"web 0.0.0.0:8080": errTest}}
	var out strings.Builder
	if err := install(h, &out, "0.0.0.0:8080", true); !errors.Is(err, errTest) {
		t.Errorf("STOP-PRECHECK-ERR: %v", err)
	}
	if h.did("systemctl stop forge-solo") || !h.running {
		t.Errorf("STOP-PRECHECK: the service was stopped before the dashboard address was checked: %q", h.calls)
	}
	if strings.Contains(out.String(), "changed") {
		t.Errorf("STOP-PRECHECK-NOTE: a change that was refused was announced:\n%s", out.String())
	}

	// The same port as now: the running service's own, checked once it has stopped.
	h = &fakeHost{unitText: unitFile(defaultWeb), running: true}
	if err := install(h, io.Discard, "0.0.0.0:3080", true); err != nil || h.did("web 0.0.0.0:3080") {
		t.Errorf("STOP-PRECHECK-OWN-PORT: the service's own port was checked while it held it: %v %q", err, h.calls)
	}

	// The service user cannot be made.
	h = &fakeHost{unitText: unitFile(defaultWeb), running: true, fail: map[string]error{"user": errTest}}
	if err := install(h, io.Discard, defaultWeb, false); !errors.Is(err, errTest) || h.did("systemctl stop forge-solo") {
		t.Errorf("STOP-PRECHECK-USER: %v, calls %q", err, h.calls)
	}

	// A service that is not running holds no port: all of them are checked first.
	h = &fakeHost{unitText: unitFile(defaultWeb), fail: map[string]error{"ports " + defaultWeb: errTest}}
	if err := install(h, io.Discard, defaultWeb, false); !errors.Is(err, errTest) || h.did("systemctl stop forge-solo") {
		t.Errorf("STOP-PRECHECK-STOPPED: %v, calls %q", err, h.calls)
	}
}

// A failure after install-service stopped the service starts it again, at the address its unit
// has, and says so; if it does not run again, the error says it is stopped and how to start it.
func TestInstallStartsTheServiceAgainAfterAFailure(t *testing.T) {
	for _, step := range []string{"ports 0.0.0.0:3080", "copy", "data", "unit", "systemctl daemon-reload", "systemctl enable forge-solo"} {
		h := &fakeHost{unitText: unitFile("0.0.0.0:3080"), running: true, fail: map[string]error{step: errTest}}
		err := install(h, io.Discard, defaultWeb, false)
		if !errors.Is(err, errTest) {
			t.Errorf("STOP-RESTORE-CAUSE: %s failed: %v", step, err)
		}
		if !h.did("systemctl start forge-solo") || !h.running || !h.did("serving 0.0.0.0:3080") {
			t.Errorf("STOP-RESTORE: %s failed and the service was not started again: %q", step, h.calls)
		}
		if err == nil || !strings.Contains(err.Error(), "has been started again") {
			t.Errorf("STOP-RESTORE-SAID: %s failed: %v", step, err)
		}
	}

	// Where the unit serves: the old address until the new unit is written.
	h := &fakeHost{unitText: unitFile("0.0.0.0:3080"), running: true, fail: map[string]error{"copy": errTest}}
	_ = install(h, io.Discard, "127.0.0.1:8080", true)
	if !h.did("serving 0.0.0.0:3080") {
		t.Errorf("STOP-RESTORE-ADDR: the service started again was looked for elsewhere: %q", h.calls)
	}

	// It does not start, or does not serve.
	for _, also := range []string{"systemctl start forge-solo", "serving 0.0.0.0:3080"} {
		h := &fakeHost{unitText: unitFile("0.0.0.0:3080"), running: true, fail: map[string]error{"copy": errTest, also: errors.New("no")}}
		err := install(h, io.Discard, defaultWeb, false)
		if err == nil || !strings.Contains(err.Error(), "is stopped") || !strings.Contains(err.Error(), "sudo systemctl start forge-solo") {
			t.Errorf("STOP-RESTORE-FAILED: %s also failed: %v", also, err)
		}
	}

	// A service that was not running is left as it was.
	h = &fakeHost{unitText: unitFile(defaultWeb), fail: map[string]error{"copy": errTest}}
	if err := install(h, io.Discard, defaultWeb, false); !errors.Is(err, errTest) || h.did("systemctl start forge-solo") {
		t.Errorf("STOP-NOT-RUNNING: %v, calls %q", err, h.calls)
	}

	// Once the new service is being started, what fails is its own: its log is shown.
	h = &fakeHost{unitText: unitFile(defaultWeb), running: true, fail: map[string]error{"systemctl restart forge-solo": errTest}}
	if err := install(h, io.Discard, defaultWeb, false); err == nil || h.did("systemctl start forge-solo") || !h.did("journal") {
		t.Errorf("STOP-NEW-SERVICE: %v, calls %q", err, h.calls)
	}
}

// --web given or not: only a given one replaces the installed service's address.
func TestParseInstallFlags(t *testing.T) {
	if web, given, err := parseInstallFlags(nil); err != nil || given || web != defaultWeb {
		t.Errorf("WEB-FLAG-ABSENT: %q %v %v", web, given, err)
	}
	if web, given, err := parseInstallFlags([]string{"--web", defaultWeb}); err != nil || !given || web != defaultWeb {
		t.Errorf("WEB-FLAG-GIVEN: --web %s: %q %v %v", defaultWeb, web, given, err)
	}
	if _, _, err := parseInstallFlags([]string{"--web", "3080"}); err == nil {
		t.Error("WEB-FLAG-BAD: --web 3080 accepted")
	}
}
