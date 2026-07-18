// +build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

var (
	appDir     string
	processes  []*exec.Cmd
	nodeCmd    *exec.Cmd
	apiCmd     *exec.Cmd
	stratumCmd *exec.Cmd
)

func main() {
	// Get application directory
	exe, _ := os.Executable()
	appDir = filepath.Dir(exe)

	// Hide console window
	hideConsole()

	// Show splash/status
	fmt.Println("Starting Forge Pool...")

	// Start services
	if err := startServices(); err != nil {
		showError("Failed to start services: " + err.Error())
		os.Exit(1)
	}

	// Open browser
	time.Sleep(3 * time.Second)
	exec.Command("cmd", "/c", "start", "http://localhost:8080").Run()

	// Wait for exit signal
	select {}
}

func hideConsole() {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	getConsoleWindow := kernel32.NewProc("GetConsoleWindow")
	user32 := windows.NewLazySystemDLL("user32.dll")
	showWindow := user32.NewProc("ShowWindow")

	hwnd, _, _ := getConsoleWindow.Call()
	if hwnd != 0 {
		showWindow.Call(hwnd, 0) // SW_HIDE
	}
}

func startServices() error {
	// Start PostgreSQL
	fmt.Println("Starting database...")
	pgDataDir := filepath.Join(appDir, "data", "pgsql")

	// Initialize if needed
	if _, err := os.Stat(filepath.Join(pgDataDir, "PG_VERSION")); os.IsNotExist(err) {
		os.MkdirAll(pgDataDir, 0755)
		initdb := exec.Command(
			filepath.Join(appDir, "pgsql", "bin", "initdb.exe"),
			"-D", pgDataDir,
			"-U", "forge",
			"-E", "UTF8",
			"--no-locale",
		)
		initdb.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := initdb.Run(); err != nil {
			return fmt.Errorf("failed to initialize database: %w", err)
		}
	}

	// Start PostgreSQL
	pgctl := exec.Command(
		filepath.Join(appDir, "pgsql", "bin", "pg_ctl.exe"),
		"-D", pgDataDir,
		"-l", filepath.Join(pgDataDir, "log.txt"),
		"start",
	)
	pgctl.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := pgctl.Run(); err != nil {
		return fmt.Errorf("failed to start database: %w", err)
	}

	time.Sleep(5 * time.Second)

	// Start BCH2 Node
	fmt.Println("Starting BCH2 node...")
	nodeDataDir := filepath.Join(appDir, "data", "node")
	os.MkdirAll(nodeDataDir, 0755)

	nodeCmd = exec.Command(
		filepath.Join(appDir, "node", "bitcoincashII-qt.exe"),
		"-datadir="+nodeDataDir,
		"-prune=1000",
		"-server=1",
		"-rpcuser=forge",
		"-rpcpassword=forgepool123",
		"-rpcallowip=127.0.0.1",
	)
	nodeCmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := nodeCmd.Start(); err != nil {
		return fmt.Errorf("failed to start node: %w", err)
	}

	// Wait for node
	fmt.Println("Waiting for node to be ready...")
	for i := 0; i < 60; i++ {
		cli := exec.Command(
			filepath.Join(appDir, "node", "bitcoincashII-cli.exe"),
			"-rpcuser=forge",
			"-rpcpassword=forgepool123",
			"getblockchaininfo",
		)
		cli.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := cli.Run(); err == nil {
			break
		}
		time.Sleep(5 * time.Second)
	}

	// Set environment variables
	os.Setenv("DB_HOST", "localhost")
	os.Setenv("DB_PORT", "5432")
	os.Setenv("DB_USER", "forge")
	os.Setenv("DB_PASSWORD", "forge")
	os.Setenv("DB_NAME", "forgepool")
	os.Setenv("RPC_HOST", "127.0.0.1")
	os.Setenv("RPC_PORT", "8342")
	os.Setenv("RPC_USER", "forge")
	os.Setenv("RPC_PASSWORD", "forgepool123")

	// Start API
	fmt.Println("Starting API server...")
	apiCmd = exec.Command(filepath.Join(appDir, "forge-api.exe"))
	apiCmd.Dir = appDir
	apiCmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := apiCmd.Start(); err != nil {
		return fmt.Errorf("failed to start API: %w", err)
	}

	time.Sleep(2 * time.Second)

	// Start Stratum
	fmt.Println("Starting Stratum server...")
	stratumCmd = exec.Command(filepath.Join(appDir, "forge-stratum.exe"))
	stratumCmd.Dir = appDir
	stratumCmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := stratumCmd.Start(); err != nil {
		return fmt.Errorf("failed to start Stratum: %w", err)
	}

	fmt.Println("Forge Pool is running!")
	return nil
}

func showError(msg string) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	messageBox := user32.NewProc("MessageBoxW")

	title, _ := syscall.UTF16PtrFromString("Forge Pool Error")
	text, _ := syscall.UTF16PtrFromString(msg)

	messageBox.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x10)
}
