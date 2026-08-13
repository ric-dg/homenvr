//go:build windows

package supervisor

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// procAttrs returns nil on Windows; process trees are killed via taskkill.
func procAttrs() *syscall.SysProcAttr { return nil }

// killTree terminates the process and its whole tree via taskkill /T /F.
func killTree(p *os.Process) {
	exec.Command("taskkill", "/PID", strconv.Itoa(p.Pid), "/T", "/F").Run()
}
