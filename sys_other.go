//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
)

// Non-Windows stubs: only used for development/testing on Linux.
func hideCmd(c *exec.Cmd) {}
func killTree(pid int)    { _ = exec.Command("kill", "-9", fmt.Sprint(pid)).Run() }
func openURL(u string) {
	if os.Getenv("USBAI_TEST_OPENLOG") != "" {
		f, _ := os.OpenFile(os.Getenv("USBAI_TEST_OPENLOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		fmt.Fprintln(f, u)
		f.Close()
	}
}
func freeRAMGB() float64        { return 2.6 }
func msgBox(title, text string) { fmt.Fprintln(os.Stderr, title+": "+text) }
