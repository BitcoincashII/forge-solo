package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// On Windows itself: a forge-gateway.exe running from the install folder is found and, when it does
// not stop, ended; one running from another folder (a Command Prompt's, say) is neither.
func TestLeftoversOnWindows(t *testing.T) {
	gatewayWorld(t, "stuck")
	installedPrograms, waitPID, killPID = installedProgramsOS, waitPIDOS, killPIDOS
	gatewayStopGrace = 500 * time.Millisecond
	elsewhere := t.TempDir()
	if err := copyHelper(filepath.Join(elsewhere, gatewayExe)); err != nil {
		t.Fatal(err)
	}
	var ours, theirs *exec.Cmd
	for _, c := range []**exec.Cmd{&ours, &theirs} {
		dir := helperDir
		if c == &theirs {
			dir = elsewhere
		}
		*c = exec.Command(filepath.Join(dir, gatewayExe))
		(*c).Env = append(os.Environ(), "GWL_HELPER_DIR="+t.TempDir())
		if err := (*c).Start(); err != nil {
			t.Fatal(err)
		}
		cmd := *c
		t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	}
	found := map[int]string{}
	for _, p := range installedProgramsOS() {
		found[p.pid] = p.exe
	}
	if found[ours.Process.Pid] != gatewayExe || found[theirs.Process.Pid] != "" || found[os.Getpid()] != "" {
		t.Fatalf("GWL-LEFTOVER-OS: the install folder's programs are %v; want process %d as %s, and neither %d nor this one", found, ours.Process.Pid, gatewayExe, theirs.Process.Pid)
	}
	stopLeftovers()
	if !waitPIDOS(ours.Process.Pid, time.Second) {
		t.Error("GWL-LEFTOVER-OS: the leftover gateway in the install folder still runs")
	}
	if waitPIDOS(theirs.Process.Pid, 200*time.Millisecond) {
		t.Error("GWL-LEFTOVER-OS: a forge-gateway.exe from another folder was ended")
	}
}
