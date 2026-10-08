//go:build !windows

package main

import "syscall"

// accessDenied is the error a program gets for a port it may not have.
var accessDenied error = syscall.EACCES

// Outside Windows, where the tray app ships, there are no listener tables, processes or services to
// read; the tests use stand-ins.
func tcpListenersOS() []tcpListener { return nil }
func processNameOS(int) string      { return "" }
func processPathOS(int) string      { return "" }
func exeNameOS(int) string          { return "" }
func forgeSoloRunsOS() bool         { return false }
func gatewayServicePIDOS() int      { return 0 }
