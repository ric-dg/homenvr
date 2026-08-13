package retention

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestFile(t *testing.T, dir, name string, size int, mtime time.Time) {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if size > 0 {
		if err := f.Truncate(int64(size)); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := now.Add(-100 * time.Hour)
	writeTestFile(t, dir, "old-1.mp4", 1000, old)
	writeTestFile(t, dir, "fresh.mp4", 1000, now.Add(-time.Hour))
	writeTestFile(t, dir, "stale-empty.mp4", 0, now.Add(-5*time.Minute))
	writeTestFile(t, dir, "fresh-empty.mp4", 0, now.Add(-10*time.Second))
	writeTestFile(t, dir, "notes.txt", 1000, old)

	var removed []string
	Run(func(msg string) { removed = append(removed, msg) }, []string{dir}, 72, now)

	remaining := map[string]bool{}
	for _, e := range dirEntries(t, dir) {
		remaining[e] = true
	}
	if remaining["old-1.mp4"] {
		t.Error("old recording not removed")
	}
	if !remaining["fresh.mp4"] {
		t.Error("fresh recording removed")
	}
	if remaining["stale-empty.mp4"] {
		t.Error("stale 0-byte file not removed")
	}
	if !remaining["fresh-empty.mp4"] {
		t.Error("recent 0-byte file removed")
	}
	if !remaining["notes.txt"] {
		t.Error("non-mp4 file removed")
	}
	if len(removed) != 2 {
		t.Errorf("logged %d removals, want 2: %v", len(removed), removed)
	}
	joined := strings.Join(removed, " | ")
	if !strings.Contains(joined, "stale 0-byte") || !strings.Contains(joined, "old-1.mp4") {
		t.Errorf("unexpected removal log lines: %v", removed)
	}
}

func TestRunDisabled(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "old.mp4", 1000, time.Now().Add(-100*time.Hour))
	Run(func(string) { t.Error("cleanup ran with retain_hours=0") }, []string{dir}, 0, time.Now())
	if len(dirEntries(t, dir)) != 1 {
		t.Error("files removed with retain_hours=0")
	}
}

func TestRunMissingDir(t *testing.T) {
	Run(func(string) { t.Error("cleanup ran on missing dir") }, []string{t.TempDir() + "\\nope"}, 72, time.Now())
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}
