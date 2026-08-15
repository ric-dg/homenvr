// Package retention implements rolling deletion of old recordings, mirroring
// v1 motion.py cleanup: .mp4 files older than record.retain_hours are removed,
// 0-byte files older than a minute are treated as aborted recordings and
// removed too, and record.retain_mb (optional) caps the total recordings size
// across all cameras by deleting the oldest first. retain_hours <= 0 disables
// the time cleanup, retain_mb <= 0 the size cleanup.
package retention

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Run deletes stale recordings from dirs. log is called for each removal.
func Run(log func(string), dirs []string, retainHours int, retainMB int64, now time.Time) {
	if retainHours <= 0 && retainMB <= 0 {
		return
	}
	if retainHours > 0 {
		cutoff := now.Add(-time.Duration(retainHours) * time.Hour)
		staleZero := now.Add(-time.Minute)
		for _, d := range dirs {
			cleanDir(log, d, cutoff, staleZero)
		}
	}
	if retainMB > 0 {
		capSize(log, dirs, retainMB)
	}
}

func cleanDir(log func(string), dir string, cutoff, staleZero time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".mp4") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, e.Name())
		mtime := info.ModTime()
		switch {
		case info.Size() == 0 && mtime.Before(staleZero):
			if err := os.Remove(path); err == nil {
				log("retention: removed stale 0-byte file " + e.Name())
			}
		case mtime.Before(cutoff):
			if err := os.Remove(path); err == nil {
				log("retention: removed " + e.Name())
			}
		}
	}
}

type recFile struct {
	path string
	size int64
	m    time.Time
}

// capSize keeps the combined size of all .mp4 files across dirs under
// limitMB, deleting the oldest first. Runs after the time-based pass so the
// time budget is honored before the size budget kicks in.
func capSize(log func(string), dirs []string, limitMB int64) {
	limit := limitMB << 20
	var files []recFile
	var total int64
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".mp4") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			files = append(files, recFile{path: filepath.Join(d, e.Name()), size: info.Size(), m: info.ModTime()})
			total += info.Size()
		}
	}
	if total <= limit {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].m.Before(files[j].m) })
	for _, f := range files {
		if total <= limit {
			return
		}
		if err := os.Remove(f.path); err == nil {
			total -= f.size
			log("retention: size cap: removed " + filepath.Base(f.path))
		}
	}
}
