//go:build !windows

package web

import "os/exec"

// detachCmd is a no-op on non-Windows platforms. The daemon there runs in the
// foreground anyway (no service manager), so the updater does not need a
// detached process group.
func detachCmd(_ *exec.Cmd) {}
