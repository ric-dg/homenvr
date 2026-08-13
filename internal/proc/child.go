// Package proc supervises external processes (go2rtc, and the per-camera
// ffmpeg captures and recorders). Child logs stdout/stderr to rotating
// <logDir>/<name>.out.log / .err.log and kills whole process trees on stop,
// mirroring v1 run.ps1's Start-Process/Stop-Proc and taskkill.
package proc

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Std stream routing values for Options.Stdout.
const (
	StdoutDiscard = ""     // stdout to /dev/null
	StdoutFile    = "file" // stdout to <name>.out.log
	StdoutPipe    = "pipe" // stdout readable via StdoutReader/ReadStdout
)

// Std stream routing values for Options.Stderr.
const (
	StderrDiscard = ""     // stderr to /dev/null
	StderrFile    = "file" // stderr to <name>.err.log
)

// Options selects how a Child connects its std streams.
type Options struct {
	// Stdout is StdoutDiscard (default), StdoutFile or StdoutPipe.
	Stdout string
	// Stderr is StderrDiscard (default) or StderrFile.
	Stderr string
	// Stdin exposes a pipe for the child's stdin (written via WriteStdin).
	Stdin bool
}

// Child is one supervised external process. Stop kills the whole process tree
// (v1 Stop-Proc / taskkill /T /F).
type Child struct {
	name   string
	logDir string

	mu     sync.Mutex
	cmd    *exec.Cmd
	out    *os.File
	err    *os.File
	stdout io.ReadCloser
	stdin  io.WriteCloser
	start  time.Time
	exited bool
}

// NewChild prepares a supervised process named name, with logs under logDir.
func NewChild(name, logDir string) *Child {
	return &Child{name: name, logDir: logDir}
}

// Start runs argv with stdout/stderr appended to log files (the common case,
// mirroring v1 Start-Proc).
func (c *Child) Start(argv []string) error {
	return c.StartOpt(argv, Options{Stdout: StdoutFile, Stderr: StderrFile})
}

// StartOpt runs argv with the requested stream routing. A reaper goroutine
// marks the child exited and closes its handles when the process terminates.
func (c *Child) StartOpt(argv []string, o Options) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil && c.cmd.Process != nil && !c.exited {
		return fmt.Errorf("%s already running (pid %d)", c.name, c.cmd.Process.Pid)
	}
	if err := os.MkdirAll(c.logDir, 0o755); err != nil {
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	switch o.Stdout {
	case StdoutFile:
		out, err := os.OpenFile(filepath.Join(c.logDir, c.name+".out.log"),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		cmd.Stdout = out
		c.out = out
	case StdoutPipe:
		r, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		c.stdout = r
	}
	if o.Stderr == StderrFile {
		errF, err := os.OpenFile(filepath.Join(c.logDir, c.name+".err.log"),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			c.closeOpenLocked()
			return err
		}
		cmd.Stderr = errF
		c.err = errF
	}
	if o.Stdin {
		w, err := cmd.StdinPipe()
		if err != nil {
			c.closeOpenLocked()
			return err
		}
		c.stdin = w
	}
	cmd.SysProcAttr = attrs()
	if err := cmd.Start(); err != nil {
		c.closeOpenLocked()
		return err
	}
	c.cmd = cmd
	c.exited = false
	c.start = time.Now()
	go func() {
		cmd.Wait()
		c.mu.Lock()
		c.exited = true
		c.closeOpenLocked()
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

// StdoutReader returns the child's piped stdout, or nil when stdout was not
// configured as a pipe. It is safe to read from a single goroutine.
func (c *Child) StdoutReader() io.Reader {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stdout
}

// ReadStdout performs a single read from the piped stdout (mirroring v1's
// detector.stdout.read(n)).
func (c *Child) ReadStdout(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stdout == nil {
		return 0, fmt.Errorf("%s: stdout not piped", c.name)
	}
	return c.stdout.Read(p)
}

// WriteStdin writes to the child's stdin pipe (only when Options.Stdin).
func (c *Child) WriteStdin(b []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stdin == nil {
		return fmt.Errorf("%s: stdin not piped", c.name)
	}
	_, err := c.stdin.Write(b)
	return err
}

// CloseStdin closes the child's stdin pipe (only when Options.Stdin).
func (c *Child) CloseStdin() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stdin == nil {
		return nil
	}
	err := c.stdin.Close()
	c.stdin = nil
	return err
}

// closeOpenLocked closes every open handle. Holding the lock guarantees no
// writer/reader is mid-call on the same handles.
func (c *Child) closeOpenLocked() {
	if c.out != nil {
		c.out.Close()
		c.out = nil
	}
	if c.err != nil {
		c.err.Close()
		c.err = nil
	}
	if c.stdout != nil {
		c.stdout.Close()
		c.stdout = nil
	}
	if c.stdin != nil {
		c.stdin.Close()
		c.stdin = nil
	}
}
