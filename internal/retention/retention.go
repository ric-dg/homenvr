// Package retention implements rolling deletion of old recordings, mirroring
// v1 motion.py cleanup: .mp4 files older than record.retain_hours are removed,
// and 0-byte files older than a minute are treated as aborted recordings and
// removed too. retain_hours <= 0 disables cleanup.
package retention

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Run deletes stale recordings from dirs. log is called for each removal.
func Run(log func(string), dirs []string, retainHours int, now time.Time) {
	if retainHours <= 0 {
		return
	}
	cutoff := now.Add(-time.Duration(retainHours) * time.Hour)
	staleZero := now.Add(-time.Minute)
	for _, d := range dirs {
		cleanDir(log, d, cutoff, staleZero)
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
