//go:build !windows

package web

import (
	"os"
	"runtime"
	"time"
)

func collectSystemInfo() SystemInfo {
	hostname, _ := os.Hostname()
	return SystemInfo{
		Hostname:  hostname,
		OS:        runtime.GOOS,
		GoVersion: runtime.Version(),
		CPUs:      runtime.NumCPU(),
		Collected: time.Now(),
	}
}
