//go:build !windows

package proc

import (
	"os"
	"os/exec"
	"syscall"
)

// attrs puts each child in its own process group so killTree can signal the
// whole group.
func attrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// killTree terminates the child's whole process group.
func killTree(p *os.Process) {
	if pg, err := syscall.Getpgid(p.Pid); err == nil && pg == p.Pid {
		syscall.Kill(-p.Pid, syscall.SIGKILL)
		return
	}
	p.Kill()
}

// KillByName force-kills every process whose image name matches name.
// Used on startup to reap orphans from a prior crash.
func KillByName(name string) {
	exec.Command("pkill", "-f", name).Run()
}
