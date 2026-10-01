package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	serviceName = "forge-solo"
	serviceUser = "forge-solo"
	serviceDir  = "/opt/forge-solo"
	serviceData = "/var/lib/forge-solo"
	unitPath    = "/etc/systemd/system/forge-solo.service"
)

// unitFile is the systemd unit install-service writes.
//
// KillMode=mixed: a stop sends SIGTERM to the launcher alone, which stops the stratum, the API
// and the node in that order (the node flushes its chain state), and only processes still running
// when TimeoutStopSec runs out are killed. With the default, systemd would signal them all at once.
func unitFile(web string) string {
	return fmt.Sprintf(`[Unit]
Description=Forge Solo: BCH2 solo mining (node, stratum and dashboard)
Documentation=https://github.com/BitcoincashII/forge-solo
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=%[1]s
ExecStart=%[2]s/forge-solo run --data-dir %[3]s --web %[4]s
KillMode=mixed
TimeoutStopSec=180
Restart=on-failure
RestartSec=10
LimitNOFILE=8192
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true

[Install]
WantedBy=multi-user.target
`, serviceUser, serviceDir, serviceData, web)
}

func haveSystemd() bool {
	st, err := os.Stat("/run/systemd/system")
	return err == nil && st.IsDir()
}

func runLoud(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// installService copies this release to /opt/forge-solo, creates the forge-solo system user and
// /var/lib/forge-solo, and installs, enables and (re)starts the systemd service. Run again from
// a newer release, it upgrades in place and keeps the data.
func installService(args []string) error {
	fs := flag.NewFlagSet("install-service", flag.ContinueOnError)
	web := fs.String("web", defaultWeb, "dashboard address, host:port; anything but 127.0.0.1 asks for a password")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := checkWebAddr(*web); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("install-service needs root: run it with sudo")
	}
	if !haveSystemd() {
		return errors.New("this system does not run systemd. Start Forge Solo from your init system instead: " +
			"as an unprivileged user that owns its data directory, run `forge-solo run --data-dir DIR`, and stop it " +
			"with SIGTERM, allowing up to 3 minutes. See README.md")
	}
	src, err := installDir()
	if err != nil {
		return err
	}
	if err := checkRelease(src); err != nil {
		return err
	}

	// Stop a running copy first: its files are about to be replaced.
	if exec.Command("systemctl", "is-active", "--quiet", serviceName).Run() == nil {
		fmt.Println("Stopping the running Forge Solo service…")
		if err := runLoud("systemctl", "stop", serviceName); err != nil {
			return err
		}
	}
	if filepath.Clean(src) != serviceDir {
		fmt.Printf("Installing %s to %s…\n", src, serviceDir)
		if err := replaceDir(src, serviceDir); err != nil {
			return err
		}
	}
	if err := ensureServiceUser(); err != nil {
		return err
	}
	if err := ensureDataDir(serviceData, serviceUser); err != nil {
		return err
	}
	if err := writeFileAtomic(unitPath, []byte(unitFile(*web)), 0o644); err != nil {
		return err
	}
	for _, a := range [][]string{{"daemon-reload"}, {"enable", serviceName}, {"restart", serviceName}} {
		if err := runLoud("systemctl", a...); err != nil {
			return err
		}
	}
	fmt.Printf(`
Forge Solo is installed and running as the %[1]s service.

  Dashboard:  http://%[2]s
  Miners:     stratum+tcp://<this machine's address>:%[3]d  (rentals: %[4]d)
  Data:       %[5]s
  Logs:       journalctl -u %[1]s -f   and %[5]s/logs/
  Stop/start: sudo systemctl stop %[1]s  /  sudo systemctl start %[1]s
`, serviceName, *web, stratumPort, rentalPort, serviceData)
	if webNeedsPassword(*web) {
		fmt.Printf("  Password:   user forge, DASHBOARD_PASSWORD in %s/secrets.env (made at first start)\n", serviceData)
	} else {
		fmt.Printf("  From another computer: ssh -L 3080:%s user@this-machine, then open http://127.0.0.1:3080\n", *web)
	}
	firewallHint()
	return nil
}

// uninstallService stops and removes the service. The program and the data stay; it says where.
func uninstallService(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("uninstall-service takes no arguments")
	}
	if os.Geteuid() != 0 {
		return errors.New("uninstall-service needs root: run it with sudo")
	}
	if !haveSystemd() {
		return errors.New("this system does not run systemd, so there is no service to remove")
	}
	if _, err := os.Stat(unitPath); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s is not installed", unitPath)
	}
	if err := runLoud("systemctl", "disable", "--now", serviceName); err != nil {
		return err
	}
	if err := os.Remove(unitPath); err != nil {
		return err
	}
	if err := runLoud("systemctl", "daemon-reload"); err != nil {
		return err
	}
	fmt.Printf(`The Forge Solo service is stopped and removed. Still on disk:
  %[1]s  (the program)
  %[2]s  (the chain, the database with your settings and blocks, and secrets.env)
Remove them with: sudo rm -r %[1]s %[2]s   and the user with: sudo userdel %[3]s
`, serviceDir, serviceData, serviceUser)
	return nil
}

// replaceDir copies src to dst through dst.new, so a failed copy leaves the old install whole.
func replaceDir(src, dst string) error {
	tmp, old := dst+".new", dst+".old"
	_ = os.RemoveAll(tmp)
	if err := copyTree(src, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	_ = os.RemoveAll(old)
	if _, err := os.Stat(dst); err == nil {
		if err := os.Rename(dst, old); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Rename(old, dst)
		return err
	}
	return os.RemoveAll(old)
}

// copyTree copies the release: regular files and directories, root-owned, not writable by others.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		t := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(t, 0o755)
		case info.Mode().IsRegular():
			mode := os.FileMode(0o644)
			if info.Mode()&0o111 != 0 {
				mode = 0o755
			}
			return copyFile(p, t, mode)
		default:
			return nil // no links or devices in a release
		}
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

// ensureServiceUser creates the unprivileged system user the service runs as.
func ensureServiceUser() error {
	if exec.Command("id", "-u", serviceUser).Run() == nil {
		return nil
	}
	shell := "/bin/false"
	for _, s := range []string{"/usr/sbin/nologin", "/sbin/nologin"} {
		if _, err := os.Stat(s); err == nil {
			shell = s
			break
		}
	}
	fmt.Printf("Creating the %s system user…\n", serviceUser)
	if _, err := exec.LookPath("useradd"); err == nil {
		return runLoud("useradd", "--system", "--user-group", "--home-dir", serviceData, "--no-create-home",
			"--shell", shell, serviceUser)
	}
	if _, err := exec.LookPath("adduser"); err == nil { // BusyBox
		return runLoud("adduser", "-S", "-D", "-H", "-h", serviceData, "-s", shell, serviceUser)
	}
	return fmt.Errorf("neither useradd nor adduser is available: create a system user named %s, then run this again", serviceUser)
}

// ensureDataDir creates the data directory for the service user, 0700.
func ensureDataDir(dir, user string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	out, err := exec.Command("id", "-u", user).Output()
	if err != nil {
		return fmt.Errorf("id -u %s: %w", user, err)
	}
	uid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return err
	}
	out, err = exec.Command("id", "-g", user).Output()
	if err != nil {
		return fmt.Errorf("id -g %s: %w", user, err)
	}
	gid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return err
	}
	// Everything under it: a data directory first used by `sudo forge-solo run` is root's.
	if err := filepath.Walk(dir, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, uid, gid)
	}); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// firewallHint names the ports to open when a host firewall that blocks them by default is on.
func firewallHint() {
	ports := fmt.Sprintf("%d, %d and %d", stratumPort, rentalPort, p2pPort)
	if exec.Command("firewall-cmd", "--state").Run() == nil {
		fmt.Printf("\nfirewalld is on and blocks incoming connections: open TCP %s for miners and peers:\n"+
			"  sudo firewall-cmd --permanent --add-port=%d/tcp --add-port=%d/tcp --add-port=%d/tcp && sudo firewall-cmd --reload\n",
			ports, stratumPort, rentalPort, p2pPort)
		return
	}
	if out, err := exec.Command("ufw", "status").Output(); err == nil && strings.Contains(string(out), "Status: active") {
		fmt.Printf("\nufw is on: open TCP %s for miners and peers:\n  sudo ufw allow %d/tcp && sudo ufw allow %d/tcp && sudo ufw allow %d/tcp\n",
			ports, stratumPort, rentalPort, p2pPort)
	}
}
