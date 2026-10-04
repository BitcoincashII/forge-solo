package main

import (
	"errors"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLockDataDirIsExclusive(t *testing.T) {
	dir := t.TempDir()
	a, err := lockDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := lockDataDir(dir); err == nil {
		b.Close()
		t.Fatal("a second lock on the same data directory succeeded")
	} else if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("error %q", err)
	}
	a.Close()
	b, err := lockDataDir(dir)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	b.Close()
}

func TestCheckRelease(t *testing.T) {
	dir := t.TempDir()
	if err := checkRelease(dir); err == nil || !strings.Contains(err.Error(), "bin/bitcoincashIId") {
		t.Fatalf("empty dir: %v", err)
	}
	for _, f := range []string{"bin/bitcoincashIId", "bin/bitcoincashII-cli", "bin/stratum", "bin/api", "web/solo.html"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkRelease(dir); err != nil {
		t.Fatal(err)
	}
}

func TestParseRunFlags(t *testing.T) {
	o, err := parseRunFlags([]string{"--data-dir", "rel/dir", "--web", "0.0.0.0:3090"})
	if err != nil || !filepath.IsAbs(o.dataDir) || !strings.HasSuffix(o.dataDir, "rel/dir") || o.web != "0.0.0.0:3090" || o.reindex {
		t.Fatalf("%+v %v", o, err)
	}
	if o, err := parseRunFlags(nil); err != nil || o.web != defaultWeb || o.reindex {
		t.Fatalf("defaults: %+v %v", o, err)
	}
	if o, err := parseRunFlags([]string{"--reindex"}); err != nil || !o.reindex {
		t.Errorf("REINDEX-FLAG: --reindex gave %+v %v", o, err)
	}
	for _, bad := range [][]string{{"--web", "3080"}, {"extra"}, {"--nope"}} {
		if _, err := parseRunFlags(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// A run as root on a data directory another account owns -- the service's, most often -- is
// refused before anything is written there: root's files broke the service's node for good.
func TestRootIsRefusedAnotherAccountsDataDir(t *testing.T) {
	dir := t.TempDir()
	if os.Geteuid() == 0 { // as root, hand the directory to another account
		if err := os.Chown(dir, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkRootDataDir(0, dir); !errors.Is(err, errRootForeignDataDir) {
		t.Errorf("ROOT-REFUSE: root on another account's data directory: %v", err)
	}
	if err := checkRootDataDir(1000, dir); err != nil {
		t.Errorf("ROOT-ONLY-ROOT: an ordinary user was refused: %v", err)
	}
	if err := checkRootDataDir(0, filepath.Join(dir, "new")); err != nil {
		t.Errorf("ROOT-NEW-DIR: root on a directory that does not exist yet: %v", err)
	}
	if err := checkRootDataDir(0, "/"); err != nil {
		t.Errorf("ROOT-OWN-DIR: root on its own directory: %v", err)
	}

	// runCmd refuses before it touches the directory.
	old := geteuid
	geteuid = func() int { return 0 }
	defer func() { geteuid = old }()
	if err := runCmd([]string{"--data-dir", dir}); !errors.Is(err, errRootForeignDataDir) {
		t.Errorf("ROOT-WIRING: forge-solo run as root on another account's data directory: %v", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("ROOT-WIRING: the refused run wrote %v", left)
	}
}

// The refusal's hints are commands that work: by name when the owner has an account, and when the
// uid has none, ones that need none (sudo -u 4242 and chown 4242: both fail). The service's
// follow whether it is installed: after uninstall-service it is not.
func TestRootRefusalHints(t *testing.T) {
	base := rootOwned{dir: "/srv/fsd", uid: 4242, gid: 4343, exe: "/home/a/rel/forge-solo", sudoUser: "alice"}
	says := func(d rootOwned) string {
		err := rootRefusal(d)
		if !errors.Is(err, errRootForeignDataDir) {
			t.Errorf("ROOT-HINT-ERR: %v", err)
		}
		return err.Error()
	}

	s := says(base)
	if !strings.Contains(s, "uid 4242, which has no account") || strings.Contains(s, "sudo -u") || strings.Contains(s, "chown -R 4242: ") {
		t.Errorf("ROOT-HINT-NOACCOUNT: a uid with no account:\n%s", s)
	}
	if !strings.Contains(s, "sudo chown -R alice: /srv/fsd") || !strings.Contains(s, "sudo chown -R 4242:4343 /srv/fsd") {
		t.Errorf("ROOT-HINT-NOACCOUNT-CHOWN: a uid with no account:\n%s", s)
	}
	d := base
	d.sudoUser = ""
	if s := says(d); !strings.Contains(s, "sudo chown -R YOUR-ACCOUNT: /srv/fsd") {
		t.Errorf("ROOT-HINT-NOSUDO: run as root without sudo:\n%s", s)
	}

	d = base
	d.name = "bob"
	if s := says(d); !strings.Contains(s, "sudo -u bob /home/a/rel/forge-solo run --data-dir /srv/fsd") || !strings.Contains(s, "sudo chown -R bob: /srv/fsd") {
		t.Errorf("ROOT-HINT-NAME: an owner with an account:\n%s", s)
	}

	d = base
	d.dir, d.service, d.unit, d.name = serviceData, true, true, serviceUser
	if s := says(d); !strings.Contains(s, "sudo systemctl start forge-solo") || !strings.Contains(s, "sudo -u forge-solo /opt/forge-solo/forge-solo run --data-dir /var/lib/forge-solo") {
		t.Errorf("ROOT-HINT-SERVICE: the installed service's data:\n%s", s)
	}
	d.unit = false
	if s := says(d); strings.Contains(s, "systemctl") || !strings.Contains(s, "not installed now") ||
		!strings.Contains(s, "sudo /home/a/rel/forge-solo install-service") || !strings.Contains(s, "sudo -u forge-solo /opt/forge-solo/forge-solo run") {
		t.Errorf("ROOT-HINT-UNINSTALLED: the data of a service that was uninstalled:\n%s", s)
	}
	d.name = "" // and its user deleted
	if s := says(d); strings.Contains(s, "sudo -u") || !strings.Contains(s, "sudo chown -R alice: /var/lib/forge-solo") {
		t.Errorf("ROOT-HINT-UNINSTALLED-NOUSER: and its user deleted:\n%s", s)
	}
}

// The owner's name as the system finds it: an account only LDAP or SSSD knows is named, which
// Forge Solo, reading /etc/passwd only, could not do.
func TestOwnerName(t *testing.T) {
	oldLookup, oldID := lookupName, idName
	t.Cleanup(func() { lookupName, idName = oldLookup, oldID })
	lookupName = func(string) (string, error) { return "", errors.New("unknown user") }
	idName = func(uid string) (string, error) {
		if uid == "4242" {
			return "carol", nil
		}
		return "", errors.New("no such user")
	}
	if n := ownerName(4242); n != "carol" {
		t.Errorf("ROOT-NAME-NSS: an account the system knows: %q", n)
	}
	if n := ownerName(4343); n != "" {
		t.Errorf("ROOT-NAME-NONE: a uid with no account: %q", n)
	}
	lookupName, idName = oldLookup, oldID
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if n := ownerName(uint32(os.Getuid())); n != me.Username {
		t.Errorf("ROOT-NAME-REAL: this account is %q, want %q", n, me.Username)
	}
	if n := ownerName(3999999); n != "" {
		t.Errorf("ROOT-NAME-REAL-NONE: uid 3999999 is %q", n)
	}
}

// The refusal knows the service's data directory, and whether the service is installed.
func TestRootRefusalSeesTheService(t *testing.T) {
	svc, other := t.TempDir(), t.TempDir()
	unit := filepath.Join(t.TempDir(), "forge-solo.service")
	if os.Geteuid() == 0 { // as root, hand the directories to another account
		for _, d := range []string{svc, other} {
			if err := os.Chown(d, 65534, 65534); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := checkRootDataDirAt(0, svc, svc, unit); err == nil || strings.Contains(err.Error(), "systemctl") || !strings.Contains(err.Error(), "not installed now") {
		t.Errorf("ROOT-WIRING-UNINSTALLED: the service's data, no unit: %v", err)
	}
	if err := os.WriteFile(unit, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkRootDataDirAt(0, svc, svc, unit); err == nil || !strings.Contains(err.Error(), "sudo systemctl start forge-solo") {
		t.Errorf("ROOT-WIRING-UNIT: the service's data, its unit installed: %v", err)
	}
	if err := checkRootDataDirAt(0, other, svc, unit); err == nil || !strings.Contains(err.Error(), "Run it as its owner") {
		t.Errorf("ROOT-WIRING-OTHER: another account's data: %v", err)
	}
}

// The BCH2 node aborts at every start on a kernel without getrandom (before Linux 3.17), before it
// has written a line. Forge Solo says so and starts nothing.
func TestKernelWithoutGetrandomIsRefused(t *testing.T) {
	err := checkKernel(unix.ENOSYS, "3.16.0-6-amd64")
	if err == nil || !strings.Contains(err.Error(), "3.17 or newer") || !strings.Contains(err.Error(), "3.16.0-6-amd64") {
		t.Errorf("KERNEL-OLD: a kernel without getrandom: %v", err)
	}
	for _, ok := range []error{nil, unix.EAGAIN, unix.EINTR} {
		if err := checkKernel(ok, "6.1.0"); err != nil {
			t.Errorf("KERNEL-OK: getrandom gave %v and the kernel was refused: %v", ok, err)
		}
	}
	if err := checkKernel(unix.EPERM, "6.1.0"); err == nil || !strings.Contains(err.Error(), "getrandom") {
		t.Errorf("KERNEL-BLOCKED: getrandom blocked by a sandbox: %v", err)
	}
	if err := checkKernel(getrandom(), kernelRelease()); err != nil {
		t.Errorf("KERNEL-THIS: this machine was refused: %v", err)
	}

	// run and install-service refuse before they do anything.
	old := getrandom
	getrandom = func() error { return unix.ENOSYS }
	defer func() { getrandom = old }()
	dir := filepath.Join(t.TempDir(), "data")
	if err := runCmd([]string{"--data-dir", dir}); err == nil || !strings.Contains(err.Error(), "3.17 or newer") {
		t.Errorf("KERNEL-RUN: forge-solo run on a kernel without getrandom: %v", err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Errorf("KERNEL-RUN: the refused run created %s", dir)
	}
	if err := installService(nil); err == nil || !strings.Contains(err.Error(), "3.17 or newer") {
		t.Errorf("KERNEL-INSTALL: install-service on a kernel without getrandom: %v", err)
	}
}

// --reindex goes to the node, at its first start only.
func TestNodeChildReindex(t *testing.T) {
	n := nodeChild("/opt/forge-solo", "/data", "/data/logs", true)
	if !reflect.DeepEqual(n.onceArgs, []string{"-reindex"}) {
		t.Errorf("REINDEX-WIRING: --reindex gave the node first-start arguments %q", n.onceArgs)
	}
	for _, a := range n.args {
		if a == "-reindex" {
			t.Errorf("REINDEX-EVERY-START: -reindex is in the arguments of every start: %q", n.args)
		}
	}
	if n := nodeChild("/opt/forge-solo", "/data", "/data/logs", false); len(n.onceArgs) != 0 {
		t.Errorf("REINDEX-UNASKED: a run without --reindex gave the node %q", n.onceArgs)
	}

	// A node that says its chain data is damaged, in its debug.log or its output, is rebuilt once;
	// a run started with --reindex has had its rebuild.
	r := nodeChild("/opt/forge-solo", "/data", "/data/logs", false).rebuild
	if r == nil || r.chainDir != "/data/bch2" || r.done ||
		!reflect.DeepEqual(r.logs, []string{"/data/bch2/debug.log", "/data/logs/node.log"}) {
		t.Errorf("REBUILD-WIRING: the node's rebuild is %+v", r)
	}
	if r := nodeChild("/opt/forge-solo", "/data", "/data/logs", true).rebuild; r == nil || !r.done {
		t.Errorf("REBUILD-WIRING-REINDEX: a run with --reindex: %+v", r)
	}
}

// A required public port another program holds is named before anything starts.
func TestCheckPublicPortsNamesTheTakenPort(t *testing.T) {
	for _, held := range []int{heldMiner, heldPeer} {
		taken := usePublicPorts(t, held)
		_, err := checkPublicPorts()
		if err == nil || !strings.Contains(err.Error(), publicPorts[held].what+" "+strconv.Itoa(taken)+" is already in use") {
			t.Errorf("PUBLIC-PORT-NAMED: with %s %d taken: %v", publicPorts[held].what, taken, err)
		}
	}
}

// Another program on the rental port leaves rentals out, as on Windows: Forge Solo still mines on
// the miners' port. It used to start nothing.
func TestRentalPortTakenLeavesRentalsOut(t *testing.T) {
	if realPublicPorts[heldMiner].port != stratumPort || realPublicPorts[heldRental].port != rentalPort || realPublicPorts[heldPeer].port != p2pPort {
		t.Fatalf("PUBLIC-PORTS-ORDER: the public ports are %v", realPublicPorts)
	}
	taken := usePublicPorts(t, heldRental)
	left, err := checkPublicPorts()
	if err != nil {
		t.Fatalf("RENTAL-OPTIONAL: with the rental port %d taken, Forge Solo refused to start: %v", taken, err)
	}
	if !reflect.DeepEqual(left, []int{taken}) {
		t.Errorf("RENTAL-LEFT-OUT: with the rental port %d taken, the ports left out are %v", taken, left)
	}
	if note := leftOutNote(taken); !strings.Contains(note, strconv.Itoa(taken)) || !strings.Contains(note, "without it") {
		t.Errorf("RENTAL-NOTE: the note does not name the port and say Forge Solo runs without it: %q", note)
	}
	usePublicPorts(t, -1)
	if left, err := checkPublicPorts(); err != nil || len(left) != 0 {
		t.Errorf("PUBLIC-PORTS-FREE: all free gave %v %v", left, err)
	}
}

// The run banner says when rentals are left out.
func TestBannerRentals(t *testing.T) {
	var b strings.Builder
	banner(&b, "127.0.0.1:3080", "/d", false, true)
	if !strings.Contains(b.String(), "MiningRigRentals: "+strconv.Itoa(rentalPort)) {
		t.Errorf("BANNER-RENTAL-ON: the banner does not give the rental port:\n%s", b.String())
	}
	b.Reset()
	banner(&b, "127.0.0.1:3080", "/d", false, false)
	if s := b.String(); !strings.Contains(s, "no rentals") || strings.Contains(s, "MiningRigRentals: "+strconv.Itoa(rentalPort)) {
		t.Errorf("BANNER-RENTAL-OFF: with the rental port taken the banner still offers it:\n%s", s)
	}
}

// Which of usePublicPorts' ports another listener holds.
const (
	heldMiner = iota
	heldRental
	heldPeer
)

// realPublicPorts is publicPorts as the program has it, before any test changes it.
var realPublicPorts = append(publicPorts[:0:0], publicPorts...)

// usePublicPorts points the public ports at free ones, the one at index held (heldMiner,
// heldRental, heldPeer; -1 for none) held by another listener for the test's duration, and
// returns that one. A machine running Forge Solo holds the real ones.
func usePublicPorts(t *testing.T, held int) (taken int) {
	t.Helper()
	saved := publicPorts
	t.Cleanup(func() { publicPorts = saved })
	ports := append(realPublicPorts[:0:0], realPublicPorts...)
	for i := range ports {
		l, err := net.Listen("tcp", ":0")
		if err != nil {
			t.Fatal(err)
		}
		ports[i].port = l.Addr().(*net.TCPAddr).Port
		if i == held {
			t.Cleanup(func() { _ = l.Close() })
			taken = ports[i].port
		} else {
			_ = l.Close()
		}
	}
	publicPorts = ports
	return taken
}

func TestRestrictDatabase(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"forgesolo.db", "forgesolo.db-wal"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	restrictDatabase(dir) // forgesolo.db-shm is absent: no error, nothing created
	for _, f := range []string{"forgesolo.db", "forgesolo.db-wal"} {
		if st, err := os.Stat(filepath.Join(dir, f)); err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v, want 0600", f, st.Mode().Perm(), err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "forgesolo.db-shm")); err == nil {
		t.Fatal("created a file that was not there")
	}
}
