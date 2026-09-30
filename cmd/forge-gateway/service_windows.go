//go:build windows

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	serviceSupported = true
	serviceName      = "ForgeGateway"
)

func isWindowsService() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

type service struct{ cfgPath string }

// Execute is the service's life: run the gateway until Windows asks it to stop.
func (s *service) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- run(s.cfgPath, stop, true) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				close(stop)
				select {
				case <-done:
				case <-time.After(20 * time.Second):
				}
				return false, 0
			}
		case err := <-done:
			if err != nil {
				// A config or login problem: the service-specific exit code makes Windows'
				// recovery actions restart it, and the reason is in the log file.
				return true, 1
			}
			return false, 0
		}
	}
}

func runService(cfgPath string) error {
	return svc.Run(serviceName, &service{cfgPath: cfgPath})
}

// serviceCommand is "install" or "uninstall", run from an Administrator prompt.
func serviceCommand(cmd string, args []string) int {
	m, err := mgr.Connect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot reach the Windows service manager (run this from an Administrator prompt):", err)
		return 1
	}
	defer m.Disconnect()

	if cmd == "uninstall" {
		s, err := m.OpenService(serviceName)
		if err != nil {
			fmt.Fprintln(os.Stderr, "the ForgeGateway service is not installed")
			return 1
		}
		defer s.Close()
		if st, err := s.Control(svc.Stop); err == nil {
			for i := 0; i < 30 && st.State != svc.Stopped; i++ {
				time.Sleep(time.Second)
				if st, err = s.Query(); err != nil {
					break
				}
			}
		}
		if err := s.Delete(); err != nil {
			fmt.Fprintln(os.Stderr, "removing the service:", err)
			return 1
		}
		fmt.Println("removed the ForgeGateway service")
		return 0
	}

	fs := flag.NewFlagSet("install", flag.ExitOnError)
	cfgPath := fs.String("config", "", `path to the config file, e.g. C:\ForgeGateway\forge-gateway.json`)
	fs.Parse(args)
	if *cfgPath == "" {
		fmt.Fprintln(os.Stderr, `install needs -config C:\path\to\forge-gateway.json`)
		return 2
	}
	abs, err := filepath.Abs(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cfg, err := loadConfig(abs)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if s, err := m.OpenService(serviceName); err == nil {
		s.Close()
		fmt.Fprintln(os.Stderr, "the ForgeGateway service is already installed; run uninstall first")
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	s, err := m.CreateService(serviceName, exe, mgr.Config{
		DisplayName: "Forge Gateway",
		Description: "Mines into Forge Pool's TIDES window from your own BCH2 node.",
		StartType:   mgr.StartAutomatic,
	}, "-config", abs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "creating the service:", err)
		return 1
	}
	defer s.Close()
	s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: time.Minute},
	}, 24*60*60)
	if err := s.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "installed, but starting it failed:", err)
		return 1
	}
	logFile := cfg.LogFile
	if logFile == "" {
		logFile = filepath.Join(filepath.Dir(abs), "forge-gateway.log")
	}
	fmt.Println("installed and started the ForgeGateway service; it starts with Windows")
	fmt.Println("log:", logFile)
	if cfg.Status.Listen != "off" {
		fmt.Println("status page: http://" + cfg.Status.Listen + "/")
	}
	return 0
}
