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
type File struct {
	path string

	mu    sync.RWMutex
	cfg   *Config
	mtime time.Time
}

// NewFile loads path (a missing file yields defaults) and watches it.
func NewFile(path string) (*File, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, err
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

func (f *File) statMtime() *time.Time {
	st, err := os.Stat(f.path)
	if err != nil {
		return nil
	}
	t := st.ModTime()
	return &t
}
