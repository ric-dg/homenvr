package supervisor

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestServiceLog_OnWrite(t *testing.T) {
	dir := t.TempDir()
	l, err := NewServiceLog(dir, 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	var mu sync.Mutex
	var lines []string
	l.OnWrite = func(line string) {
		mu.Lock()
		lines = append(lines, line)
		mu.Unlock()
	}

	l.Logf("test message %d", 1)
	l.Logf("test message %d", 2)

	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if lines[0] == "" || lines[1] == "" {
		t.Errorf("empty line received: %q %q", lines[0], lines[1])
	}
}

func TestServiceLog_OnWrite_Nil(t *testing.T) {
	dir := t.TempDir()
	l, err := NewServiceLog(dir, 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// OnWrite is nil — should not panic.
	l.Logf("no callback %d", 1)
}

func TestServiceLog_RotationTriggersOnWrite(t *testing.T) {
	dir := t.TempDir()
	// Tiny max size so rotation triggers quickly.
	l, err := NewRotatingLog(dir, "test.log", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	var mu sync.Mutex
	count := 0
	l.OnWrite = func(line string) {
		mu.Lock()
		count++
		mu.Unlock()
	}

	// Write enough to trigger rotation.
	for i := 0; i < 200; i++ {
		l.Logf("rotation test line %d with some padding to fill bytes", i)
	}

	mu.Lock()
	defer mu.Unlock()
	if count < 200 {
		t.Errorf("OnWrite called %d times, want >= 200", count)
	}
}

func TestServiceLog_FileContent(t *testing.T) {
	dir := t.TempDir()
	l, err := NewServiceLog(dir, 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	l.Logf("hello world")

	data, err := os.ReadFile(filepath.Join(dir, "service.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("log file is empty")
	}
}
