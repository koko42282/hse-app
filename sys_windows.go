//go:build windows

package main

import (
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"
)

const createNoWindow = 0x08000000

func hideCmd(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

func killTree(pid int) {
	c := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	hideCmd(c)
	_ = c.Run()
}

func openURL(u string) {
	c := exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	hideCmd(c)
	_ = c.Start()
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func freeRAMGB() float64 {
	k := syscall.NewLazyDLL("kernel32.dll")
	p := k.NewProc("GlobalMemoryStatusEx")
	var m memoryStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	r, _, _ := p.Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return 0
	}
	return float64(m.AvailPhys) / (1 << 30)
}

func msgBox(title, text string) {
	u := syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW")
	t, _ := syscall.UTF16PtrFromString(text)
	c, _ := syscall.UTF16PtrFromString(title)
	_, _, _ = u.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), 0x10)
}
