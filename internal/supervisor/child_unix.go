//go:build !windows

package supervisor

import (
	"os"
	"syscall"
)

// procAttrs puts each child in its own process group so killTree can signal
// the whole group.
func procAttrs() *syscall.SysProcAttr {
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
