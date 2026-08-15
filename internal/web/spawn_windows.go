//go:build windows

package web

import (
	"os/exec"
	"syscall"
)

// DETACHED_PROCESS (0x00000008) creates the child without a console; not in
// the stdlib syscall package, so defined here. The updater helper must
// outlive the daemon process that spawns it.
const detachedProcessFlag = 0x00000008

// detachCmd makes cmd independent of the daemon's console and process group,
// so the updater can swap the exe and restart the service after the daemon
// exits. Windows-only: CREATE_NEW_PROCESS_GROUP and DETACHED_PROCESS are not
// portable.
func detachCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcessFlag}
}
