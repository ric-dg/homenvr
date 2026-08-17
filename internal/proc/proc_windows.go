//go:build windows

package proc

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// attrs returns nil on Windows; process trees are killed via taskkill.
func attrs() *syscall.SysProcAttr { return nil }

// killTree terminates the process and its whole tree via taskkill /T /F.
func killTree(p *os.Process) {
	exec.Command("taskkill", "/PID", strconv.Itoa(p.Pid), "/T", "/F").Run()
}

// KillByName force-kills every process whose image name matches exe
// (e.g. "go2rtc.exe"). Used on startup to reap orphans from a prior crash.
func KillByName(exe string) {
	exec.Command("taskkill", "/IM", exe, "/F").Run()
}
