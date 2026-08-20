package config

import (
	"os"
	"sync"
	"time"
)

// File wraps a config path with mtime-based hot reload, mirroring v1's Config
// class. Get returns the last successfully loaded config; Refresh reloads only
// when the file changed on disk and only swaps the active config on a
// successful parse (a broken edit keeps the last good config).
//
// Auto-recovery: on every successful Save, the raw bytes are written to
// path+".last-good". If a future Load fails (parse error), the backup is
// restored so the daemon can start with the last known good config.
type File struct {
	path string

	mu    sync.RWMutex
	cfg   *Config
	mtime time.Time
}

// NewFile loads path (a missing file yields defaults) and watches it.
// If path is corrupt, it tries path+".last-good" before giving up.
func NewFile(path string) (*File, error) {
	cfg, err := Load(path)
	if err != nil {
		// Try the last-known-good backup before giving up.
		backup := path + ".last-good"
		if _, bErr := os.Stat(backup); bErr == nil {
			cfg, err = Load(backup)
			if err == nil {
				// Restore the backup as the main config (best-effort).
				if data, rErr := os.ReadFile(backup); rErr == nil {
					_ = os.WriteFile(path, data, 0o644)
				}
			}
		}
		if err != nil {
			return nil, err
		}
	}
	f := &File{path: path, cfg: cfg}
	if m := f.statMtime(); m != nil {
		f.mtime = *m
	}
	return f, nil
}

// Get returns the current config.
func (f *File) Get() *Config {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.cfg
}

// Refresh reloads the config if the file changed. It reports whether a new
// config was applied, or false when nothing changed or the file is unreadable.
// On a parse error the previous config is kept.
func (f *File) Refresh() (bool, error) {
	m := f.statMtime()
	if m == nil || m.Equal(f.mtime) {
		return false, nil
	}
	cfg, err := Load(f.path)
	if err != nil {
		return false, err
	}
	f.mu.Lock()
	f.cfg = cfg
	f.mtime = *m
	f.mu.Unlock()
	return true, nil
}

// Path returns the watched file path.
func (f *File) Path() string { return f.path }

// Save writes data to the config file and updates the backup. This is used
// by the web panel to atomically save and keep a last-known-good copy.
func (f *File) Save(data []byte) error {
	if err := writeAtomic(f.path, data); err != nil {
		return err
	}
	// Best-effort backup — don't fail the save if backup write fails.
	_ = writeAtomic(f.path+".last-good", data)
	// Bump mtime so the supervisor's next Refresh() picks up the change.
	// Do NOT call Load here — it probes ffmpeg synchronously and blocks the
	// HTTP response. The hot-reload tick handles normalization.
	f.mu.Lock()
	f.mtime = time.Now()
	f.mu.Unlock()
	return nil
}

func (f *File) statMtime() *time.Time {
	st, err := os.Stat(f.path)
	if err != nil {
		return nil
	}
	t := st.ModTime()
	return &t
}

// writeAtomic writes data to path via temp file + rename for crash safety.
// On Windows the target is removed first to avoid rename failures.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	// Remove before rename: Go's os.Rename does not overwrite on Windows.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
