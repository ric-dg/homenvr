package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ServiceLog appends timestamped lines to <dir>/service.log and rotates it
// once it reaches maxMB, keeping up to keep rotated copies. Mirrors v1's
// Log helper plus Rotate-LogFile for service.log.
type ServiceLog struct {
	mu   sync.Mutex
	dir  string
	file string
	f    *os.File
	max  int64
	keep int
}

// NewServiceLog opens (creating if needed) service.log under dir.
func NewServiceLog(dir string, maxMB, keep int) (*ServiceLog, error) {
	return NewRotatingLog(dir, "service.log", maxMB, keep)
}

// NewRotatingLog opens (creating if needed) one rotating log file (e.g.
// service.log or alert.log) under dir.
func NewRotatingLog(dir, name string, maxMB, keep int) (*ServiceLog, error) {
	if keep < 1 {
		keep = 1
	}
	l := &ServiceLog{dir: dir, file: name, max: int64(maxMB) * 1024 * 1024, keep: keep}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

// UpdateLogDir relocates the log file when paths.log_dir changes (hot reload).
func (l *ServiceLog) UpdateLogDir(dir string, maxMB, keep int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if dir == l.dir {
		return
	}
	l.dir = dir
	l.max = int64(maxMB) * 1024 * 1024
	if keep < 1 {
		keep = 1
	}
	l.keep = keep
	l.closeLocked()
	if err := l.open(); err != nil {
		fmt.Fprintf(os.Stderr, "service log reopen: %v\n", err)
	}
}

// Logf writes a timestamped line to service.log, rotating first if needed.
func (l *ServiceLog) Logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		l.open()
	}
	if l.f == nil {
		fmt.Fprintf(os.Stderr, "service log: %s\n", msg)
		return
	}
	ts := time.Now().Format("2006-01-02 15:04:05")
	fmt.Fprintf(l.f, "%s  %s\n", ts, msg)
	l.rotateLocked()
}

// Dir returns the current log directory.
func (l *ServiceLog) Dir() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dir
}

func (l *ServiceLog) open() error {
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(l.dir, l.file),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	l.f = f
	return nil
}

func (l *ServiceLog) closeLocked() {
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}

func (l *ServiceLog) rotateLocked() {
	if l.f == nil || l.max <= 0 {
		return
	}
	st, err := l.f.Stat()
	if err != nil || st.Size() < l.max {
		return
	}
	l.closeLocked()
	path := filepath.Join(l.dir, l.file)
	for i := l.keep - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", path, i)
		dst := fmt.Sprintf("%s.%d", path, i+1)
		if _, err := os.Stat(src); err == nil {
			os.Rename(src, dst)
		}
	}
	os.Rename(path, path+".1")
	l.open()
}
