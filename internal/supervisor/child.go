package supervisor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Child is one supervised external process (go2rtc, or any ffmpeg feed).
// Its stdout/stderr are appended to <logDir>/<name>.out.log and .err.log,
// mirroring v1's Start-Process -RedirectStandardOutput/-Error. Stop kills the
// whole process tree (v1 $p.Kill($true)).
type Child struct {
	name   string
	logDir string

	mu     sync.Mutex
	cmd    *exec.Cmd
	out    *os.File
	err    *os.File
	start  time.Time
	exited bool
}

// NewChild prepares a supervised process named name, with logs under logDir.
func NewChild(name, logDir string) *Child {
	return &Child{name: name, logDir: logDir}
}

// Start launches argv, opening fresh stdout/stderr log files first. A reaper
// goroutine marks the child exited when the process terminates.
func (c *Child) Start(argv []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil && c.cmd.Process != nil && !c.exited {
		return fmt.Errorf("%s already running (pid %d)", c.name, c.cmd.Process.Pid)
	}
	if err := os.MkdirAll(c.logDir, 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(filepath.Join(c.logDir, c.name+".out.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	errF, err := os.OpenFile(filepath.Join(c.logDir, c.name+".err.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		out.Close()
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = out
	cmd.Stderr = errF
	cmd.SysProcAttr = procAttrs()
	if err := cmd.Start(); err != nil {
		out.Close()
		errF.Close()
		return err
	}
	c.cmd, c.out, c.err = cmd, out, errF
	c.exited = false
	c.start = time.Now()
	go func() {
		cmd.Wait()
		c.mu.Lock()
		c.exited = true
		c.closeFilesLocked()
		c.mu.Unlock()
	}()
	return nil
}

// Name returns the child's name.
func (c *Child) Name() string { return c.name }

// LogDir returns the child's log directory.
func (c *Child) LogDir() string { return c.logDir }

// Pid returns the process id, or 0 when not running.
func (c *Child) Pid() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil && c.cmd.Process != nil {
		return c.cmd.Process.Pid
	}
	return 0
}

// Exited reports whether the process has terminated.
func (c *Child) Exited() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exited
}

// ExitCode returns the process exit code, or -1 if unknown.
func (c *Child) ExitCode() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil && c.cmd.ProcessState != nil {
		return c.cmd.ProcessState.ExitCode()
	}
	return -1
}

// Uptime returns how long the process has been running.
func (c *Child) Uptime() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd == nil || c.cmd.Process == nil || c.exited {
		return 0
	}
	return time.Since(c.start)
}

// Stop terminates the process tree and reaps it. It is idempotent.
func (c *Child) Stop() {
	c.mu.Lock()
	cmd := c.cmd
	c.mu.Unlock()
	if cmd == nil || cmd.Process == nil || c.exited {
		return
	}
	killTree(cmd.Process)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		ex := c.exited
		c.mu.Unlock()
		if ex {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	cmd.Process.Kill()
}

func (c *Child) closeFilesLocked() {
	if c.out != nil {
		c.out.Close()
		c.out = nil
	}
	if c.err != nil {
		c.err.Close()
		c.err = nil
	}
}
