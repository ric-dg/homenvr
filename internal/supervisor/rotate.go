package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
)

// RotateManagedLogs implements v1 Rotate-ManagedLogs: it rotates service.log
// and every *.out.log / *.err.log under dir that is not currently held open
// by a running child. Files that are busy (children still writing) are
// skipped; they get fresh files on the next process restart anyway. Returns
// the names that were rotated.
func RotateManagedLogs(dir string, maxMB, keep int) []string {
	var rotated []string
	if maxMB <= 0 {
		return rotated
	}
	if keep < 1 {
		keep = 1
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return rotated
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".log" {
			continue
		}
		if e.Name() != "service.log" &&
			!endsWithSuffix(e.Name(), ".out.log") &&
			!endsWithSuffix(e.Name(), ".err.log") {
			continue
		}
		if ok := RotateFile(filepath.Join(dir, e.Name()), maxMB, keep); ok {
			rotated = append(rotated, e.Name())
		}
	}
	return rotated
}

// RotateFile rotates a single log file when it exceeds maxMB, shifting
// existing .N copies down (v1 Rotate-LogFile). Returns false when the file is
// too small, missing, or busy.
func RotateFile(path string, maxMB, keep int) bool {
	st, err := os.Stat(path)
	if err != nil || st.Size() < int64(maxMB)*1024*1024 {
		return false
	}
	for i := keep - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", path, i)
		dst := fmt.Sprintf("%s.%d", path, i+1)
		if _, err := os.Stat(src); err == nil {
			if err := os.Rename(src, dst); err != nil {
				return false
			}
		}
	}
	if err := os.Rename(path, path+".1"); err != nil {
		return false
	}
	return true
}

func endsWithSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
