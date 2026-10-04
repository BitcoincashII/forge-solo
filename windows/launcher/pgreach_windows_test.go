package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// A user name the code page holds (Jürgen on a Western Windows) is given to PostgreSQL as it is, as
// 1.0.12 did; one in another script is not.
func TestACPRepresentableOnWindows(t *testing.T) {
	if acp := windows.GetACP(); acp != 1252 {
		t.Skipf("code page %d: the 1252 cases do not apply", acp)
	}
	if !codePageHolds(`C:\Users\J` + "\u00fc" + `rgen`) {
		t.Error("PGREACH-WINDOWS-ACP: code page 1252 does not hold Jürgen")
	}
	if codePageHolds(`C:\Users\` + "\u7528\u6237") {
		t.Error("PGREACH-WINDOWS-ACP: code page 1252 holds a Chinese name")
	}
}

// A junction made by the launcher, without administrator rights, leads to its folder (a name in
// another script), and os.Remove removes the junction alone: the folder and its files stay.
func TestJunctionOnWindows(t *testing.T) {
	target := filepath.Join(t.TempDir(), "data-"+cn)
	md(filepath.Join(target, "pgdata"))
	if err := os.WriteFile(filepath.Join(target, "pgdata", "PG_VERSION"), []byte("16\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "data")
	if err := makeJunction(link, target); err != nil {
		t.Fatalf("JUNCTION-WINDOWS-MAKE: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(link, "pgdata", "PG_VERSION")); err != nil || string(b) != "16\n" {
		t.Fatalf("JUNCTION-WINDOWS-LEADS: %q %v", b, err)
	}
	a, _ := os.Stat(link)
	b, _ := os.Stat(target)
	if !os.SameFile(a, b) {
		t.Fatal("JUNCTION-WINDOWS-SAME: the junction does not lead to its folder")
	}
	if err := os.Remove(link); err != nil {
		t.Fatalf("JUNCTION-WINDOWS-REMOVE: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("JUNCTION-WINDOWS-REMOVED: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(target, "pgdata", "PG_VERSION")); err != nil || string(b) != "16\n" {
		t.Fatalf("JUNCTION-WINDOWS-TARGET-KEPT: removing the junction took the folder's files: %v", err)
	}
}

// pgRun runs one of the bundled PostgreSQL programs for the test, with the old database's password.
// Its output goes to a file, not a pipe: the server pg_ctl starts keeps what it inherits open.
func pgRun(t *testing.T, r *pgReach, name string, args ...string) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "pg-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	c := pgCmd(r, name, args...)
	c.Env = append(os.Environ(), "PGPASSWORD="+sec.DBPass)
	c.Stdout, c.Stderr = out, out
	if err := c.Run(); err != nil {
		b, _ := os.ReadFile(out.Name())
		t.Fatalf("%s %v: %v\n%s", name, args, err, b)
	}
}

// The whole move on Windows, with the real bundled PostgreSQL (FS_PGSQL: an installed pgsql folder)
// and forge-solo-migrate.exe (FS_MIGRATE), from an install folder and a data folder whose names
// PostgreSQL cannot take, on a drive that keeps no short names: through junctions. An earlier
// version's database is made there as 1.0.12 made it; prepareDatabase moves it, stops PostgreSQL,
// removes the junctions, and, the move checked, the bundled PostgreSQL. The next start needs
// nothing.
func TestMoveThroughAJunctionOnWindows(t *testing.T) {
	pgsql, migrator := os.Getenv("FS_PGSQL"), os.Getenv("FS_MIGRATE")
	if pgsql == "" || migrator == "" {
		t.Skip("FS_PGSQL (an installed pgsql folder) and FS_MIGRATE (forge-solo-migrate.exe) are not set")
	}
	root := t.TempDir()
	savedInst, savedData, savedSec, savedPort, savedShort, savedTip := installDir, dataDir, sec, pgPort, shortName, setTooltip
	installDir, dataDir = filepath.Join(root, "fs-"+cn), filepath.Join(root, "data-"+cn)
	sec = secrets{DBPass: "test-password"}
	shortName = func(string) (string, error) { return "", errors.New("this drive keeps no short names") }
	setTooltip = func(string) {} // no tray here
	t.Cleanup(func() {
		stopDatabase()
		installDir, dataDir, sec, pgPort, shortName, setTooltip = savedInst, savedData, savedSec, savedPort, savedShort, savedTip
	})
	pd := filepath.Join(root, "ProgramData")
	md(pd)
	t.Setenv("ProgramData", pd)
	if err := copyTree(pgsql, ipath("pgsql")); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(migrator, ipath(migrateExe)); err != nil {
		t.Fatal(err)
	}
	md(dataDir)

	// The earlier version's database, as 1.0.12 made it, through the junctions.
	r, err := reachPostgres()
	if err != nil || len(r.links) != 2 {
		t.Fatalf("MOVE-WINDOWS-JUNCTIONS: %+v %v", r, err)
	}
	if err := os.WriteFile(dpath("pgpw.txt"), []byte(sec.DBPass), 0o600); err != nil {
		t.Fatal(err)
	}
	pgRun(t, r, "initdb.exe", "-D", filepath.Join(r.data, "pgdata"), "-U", "forge", "-A", "scram-sha-256",
		"--pwfile", filepath.Join(r.data, "pgpw.txt"), "-E", "UTF8", "--no-locale")
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	_, port, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	pgRun(t, r, "pg_ctl.exe", "-D", filepath.Join(r.data, "pgdata"), "-l", filepath.Join(r.data, "pglog.txt"),
		"-o", "-p "+port+" -h 127.0.0.1", "-w", "-t", "120", "start")
	schema, err := os.ReadFile(filepath.Join(pgmigrateTestdata, "pg-1.0.12-schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	seed := string(schema) + `
INSERT INTO pool_config (id, pool_address, coinbase_tag, payout_mode, updated_at) VALUES (1, 'bitcoincashii:qwindows', '/w-tag/', 'solo', '2026-09-30 12:00:00.4+00');
INSERT INTO miners (address, solo_mining, address_1175, settings_pin_hash) VALUES ('bitcoincashii:qwindows', true, 'es1qwindows', 'pin');
INSERT INTO blocks (height, hash, miner_address, reward, status, is_solo, created_at) VALUES
  (100, repeat('a', 64), 'bitcoincashii:qwindows', 50, 'confirmed', true, '2026-09-29 10:00:00+00'),
  (101, repeat('b', 64), 'bitcoincashii:qwindows', 50, 'pending', true, '2026-09-30 10:00:00+00');
INSERT INTO payouts (miner_address, block_height, amount, confirmed, status, txid) VALUES ('bitcoincashii:qwindows', 100, 50, true, 'paid', 'coinbase-direct');
`
	if err := os.WriteFile(dpath("seed.sql"), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	pgRun(t, r, "psql.exe", "-h", "127.0.0.1", "-p", port, "-U", "forge", "-d", "postgres", "-c", "CREATE DATABASE forgesolo")
	pgRun(t, r, "psql.exe", "-h", "127.0.0.1", "-p", port, "-U", "forge", "-d", "forgesolo", "-v", "ON_ERROR_STOP=1",
		"-f", filepath.Join(r.data, "seed.sql"))
	pgRun(t, r, "pg_ctl.exe", "-D", filepath.Join(r.data, "pgdata"), "-m", "fast", "-w", "stop")
	r.remove()

	// The move, as a start makes it.
	prepareDatabase()
	log, _ := os.ReadFile(dpath("launcher.log"))
	s, ok, err := readStatus()
	if !ok || err != nil || s.State != stateDone {
		t.Fatalf("MOVE-WINDOWS-DONE: status %+v (%v); log:\n%s", s, err, log)
	}
	if !strings.Contains(string(log), "so it is given the junction") {
		t.Errorf("MOVE-WINDOWS-THROUGH-JUNCTION: launcher.log does not say the move went through a junction:\n%s", log)
	}
	var m struct {
		Counts map[string]int `json:"counts"`
	}
	b, _ := os.ReadFile(dpath(markerName))
	if err := json.Unmarshal(b, &m); err != nil || m.Counts["blocks"] != 2 || m.Counts["payouts"] != 1 || m.Counts["miners"] != 1 {
		t.Errorf("MOVE-WINDOWS-COUNTS: postgres-migrated.json says %s (%v)", b, err)
	}
	if postmasterPID() != 0 {
		t.Errorf("MOVE-WINDOWS-STOPPED: PostgreSQL still runs after the move")
	}
	if ents, _ := os.ReadDir(filepath.Join(pd, "ForgeSolo", "links")); len(ents) != 0 {
		t.Errorf("MOVE-WINDOWS-JUNCTIONS-REMOVED: %v are left", ents)
	}
	if _, err := os.Stat(ipath("pgsql")); !os.IsNotExist(err) {
		t.Errorf("MOVE-WINDOWS-PGSQL-REMOVED: the bundled PostgreSQL is still installed after a checked move (%v)", err)
	}
	if _, err := os.Stat(dpath("pgdata", "global", "pg_control")); err != nil {
		t.Errorf("MOVE-WINDOWS-OLD-DATA-KEPT: %v", err)
	}
	if action, _, verified := quickPlan(dbPath(), dpath("pgdata")); action != planNone || !verified {
		t.Errorf("MOVE-WINDOWS-NEXT-START: the next start would do %q (verified %v)", action, verified)
	}
	out, err := exec.Command(ipath(migrateExe), "plan", "--db", dbPath(), "--pgdata", dpath("pgdata")).Output()
	if err != nil || strings.TrimSpace(string(out)) != "none" {
		t.Errorf("MOVE-WINDOWS-MIGRATOR-AGREES: forge-solo-migrate plan says %q (%v)", out, err)
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
