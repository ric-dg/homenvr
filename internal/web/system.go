package web

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// SystemInfo is the runtime machine snapshot.
type SystemInfo struct {
	Hostname   string     `json:"hostname"`
	OS         string     `json:"os"`
	GoVersion  string     `json:"go_version"`
	CPUs       int        `json:"cpus"`
	Memory     MemInfo    `json:"memory"`
	Disk       DiskInfo   `json:"disk"`
	GPU        string     `json:"gpu"`
	Collecting string     `json:"-"`
	Collected  time.Time  `json:"-"`
}

type MemInfo struct {
	Total     uint64  `json:"total_bytes"`
	Used      uint64  `json:"used_bytes"`
	Free      uint64  `json:"free_bytes"`
	UsedPct   float64 `json:"used_pct"`
}

type DiskInfo struct {
	Total   uint64  `json:"total_bytes"`
	Free    uint64  `json:"free_bytes"`
	Used    uint64  `json:"used_bytes"`
	UsedPct float64 `json:"used_pct"`
}

var (
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetDiskFreeSpaceExW  = kernel32.NewProc("GetDiskFreeSpaceExW")
)

// https://learn.microsoft.com/en-us/windows/win32/api/sysinfoapi/ns-sysinfoapi-memorystatusex
type memoryStatusEx struct {
	DwLength     uint32
	DwMemoryLoad uint32
	ullTotalPhys uint64
	ullAvailPhys uint64
	ullTotalPage uint64
	ullAvailPage uint64
	ullTotalVirtual uint64
	ullAvailVirtual uint64
	_ [8]byte   // ullAvailExtendedVirtual (reserved)
}

func collectSystemInfo() SystemInfo {
	hostname, _ := os.Hostname()
	st := SystemInfo{
		Hostname:  hostname,
		OS:        runtime.GOOS,
		GoVersion: runtime.Version(),
		CPUs:      runtime.NumCPU(),
		Collected: time.Now(),
	}

	// Memory via GlobalMemoryStatusEx
	var mem memoryStatusEx
	mem.DwLength = uint32(unsafe.Sizeof(mem))
	procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&mem)))
	if mem.DwLength > 0 {
		st.Memory = MemInfo{
			Total:   mem.ullTotalPhys,
			Used:    mem.ullTotalPhys - mem.ullAvailPhys,
			Free:    mem.ullAvailPhys,
			UsedPct: float64(mem.DwMemoryLoad),
		}
	}

	// Disk for C:\
	st.Disk = diskInfo("C:\\")

	// GPU via nvidia-smi
	st.GPU = detectGPU()

	return st
}

func diskInfo(path string) DiskInfo {
	var freeBytesAvailable, totalBytes, totalFreeBytes uint64
	p, _ := syscall.UTF16PtrFromString(path)
	procGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)
	if totalBytes == 0 {
		return DiskInfo{}
	}
	return DiskInfo{
		Total:   totalBytes,
		Free:    totalFreeBytes,
		Used:    totalBytes - totalFreeBytes,
		UsedPct: float64(totalBytes-totalFreeBytes) / float64(totalBytes) * 100,
	}
}

func detectGPU() string {
	out, err := exec.Command("nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader,nounits").CombinedOutput()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[0])
}

// systemInfoCache holds a periodically refreshed snapshot so the status
// endpoint never has to block on slow syscalls.
var (
	systemCacheMu   sync.Mutex
	systemCache     SystemInfo
	systemCacheTime time.Time
)

// CollectSystemInfo returns a cached system snapshot, refreshing at most
// every 30 seconds. Safe for concurrent use.
func CollectSystemInfo() SystemInfo {
	systemCacheMu.Lock()
	defer systemCacheMu.Unlock()
	if time.Since(systemCacheTime) < 30*time.Second && systemCacheTime.IsZero() == false {
		return systemCache
	}
	systemCache = collectSystemInfo()
	systemCacheTime = time.Now()
	return systemCache
}
