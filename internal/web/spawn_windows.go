//go:build windows

package web

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW (0x08000000) keeps the updater from flashing a console in
// an interactive session; not in the stdlib syscall package, so defined here.
const noWindowFlag = 0x08000000

// detachCmd makes cmd independent of the daemon's console and process group,
// so the updater can swap the exe and restart the service after the daemon
// exits. Windows-only: CREATE_NEW_PROCESS_GROUP and CREATE_NO_WINDOW are not
// portable.
//
// DETACHED_PROCESS is deliberately NOT used: under Go's exec it creates a
// child that exits immediately without running (verified empirically), so the
// updater would never swap the binary.
func detachCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | noWindowFlag}
}
