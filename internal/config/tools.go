package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// findTool resolves a bundled binary the same way v1 did: an explicitly
// configured existing path wins, then a copy next to our own executable, then
// PATH, then (Windows) scoop installs. Returns "" when nothing is found.
func findTool(name, configured string) string {
	if configured != "" {
		if st, err := os.Stat(configured); err == nil && !st.IsDir() {
			return configured
		}
	}
	if exe, err := os.Executable(); err == nil {
		own := filepath.Join(filepath.Dir(exe), exeName(name))
		if st, err := os.Stat(own); err == nil && !st.IsDir() {
			return own
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	if runtime.GOOS == "windows" {
		if p, err := exec.LookPath(exeName(name)); err == nil {
			return p
		}
		for _, root := range []string{
			`C:\ProgramData\scoop\apps`,
			filepath.Join(os.Getenv("USERPROFILE"), "scoop", "apps"),
		} {
			for _, sub := range []string{"", "bin"} {
				cand := filepath.Join(root, name, "current", sub, exeName(name))
				if st, err := os.Stat(cand); err == nil && !st.IsDir() {
					return cand
				}
			}
		}
		if name == "pwsh" {
			cand := `C:\Program Files\PowerShell\7\pwsh.exe`
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				return cand
			}
		}
	}
	return ""
}

// resolveTools fills every Tools field that is still empty or invalid.
func (c *Config) resolveTools() {
	c.Tools.FFmpeg = findTool("ffmpeg", c.Tools.FFmpeg)
	c.Tools.Go2rtc = findTool("go2rtc", c.Tools.Go2rtc)
	c.Tools.Python = findTool("python", c.Tools.Python)
	c.Tools.Pwsh = findTool("pwsh", c.Tools.Pwsh)
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}
