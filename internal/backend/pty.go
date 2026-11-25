package backend

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/uatx/tterminal/internal/types"
	"golang.org/x/sys/unix"
)

type PTYOptions struct {
	Argv             []string
	Shell            string
	Cwd              string
	Env              []string
	Term             string
	Login            bool
	Cols             int
	Rows             int
	ShellIntegration bool
}

type PTYBackend struct {
	opts   PTYOptions
	cmd    *exec.Cmd
	master *os.File
	slave  *os.File

	mu       sync.Mutex
	status   types.BackendStatus
	statusCh chan struct{}
	closeOne sync.Once
	cleanup  []string
}

func NewPTY(opts PTYOptions) *PTYBackend {
	if opts.Cols <= 0 {
		opts.Cols = 80
	}
	if opts.Rows <= 0 {
		opts.Rows = 24
	}
	if opts.Term == "" {
		opts.Term = "xterm-256color"
	}
	return &PTYBackend{
		opts:     opts,
		statusCh: make(chan struct{}),
		status: types.BackendStatus{
			State:     "initializing",
			Caps:      ptyCaps(),
			StartedAt: time.Now(),
		},
	}
}

func ptyCaps() types.BackendCaps {
	return types.BackendCaps{HasProcess: true, CanSignal: true, CanResize: true, CanReportExit: true}
}

func (b *PTYBackend) Kind() types.BackendKind         { return types.BackendPTY }
func (b *PTYBackend) Capabilities() types.BackendCaps { return ptyCaps() }

func (b *PTYBackend) Start(ctx context.Context) error {
	if len(b.opts.Argv) == 0 {
		return fmt.Errorf("invalid_argument: argv is required for pty backend")
	}
	argv := append([]string(nil), b.opts.Argv...)
	env := append([]string(nil), b.opts.Env...)
	env = append(env, "TERM="+b.opts.Term, "TTERMINAL_BACKEND=pty")

	if b.opts.ShellIntegration {
		rc, dir, err := shellIntegrationFiles(filepath.Base(argv[0]))
		if err != nil {
			return err
		}
		if rc != "" {
			b.cleanup = append(b.cleanup, dir)
			switch filepath.Base(argv[0]) {
			case "bash":
				argv = append([]string{argv[0], "--rcfile", rc}, argv[1:]...)
			case "zsh":
				env = append(env, "ZDOTDIR="+dir)
			case "fish":
				argv = append(argv, "-C", shellFishRC)
			default:
				env = append(env, "ENV="+rc)
			}
		}
		env = append(env, "TTERMINAL_SHELL_INTEGRATION=1")
	}

	master, slave, err := pty.Open()
	if err != nil {
		return fmt.Errorf("pty_open: %w", err)
	}
	if err := pty.Setsize(slave, &pty.Winsize{Rows: uint16(b.opts.Rows), Cols: uint16(b.opts.Cols)}); err != nil {
		master.Close()
		slave.Close()
		return fmt.Errorf("pty_resize: %w", err)
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = b.opts.Cwd
	cmd.Env = env
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		master.Close()
		slave.Close()
		return fmt.Errorf("process_start: %w", err)
	}
	slave.Close()

	b.mu.Lock()
	b.master = master
	b.cmd = cmd
	b.status.State = "running"
	b.status.PID = cmd.Process.Pid
	b.mu.Unlock()

	go b.waitLoop()
	return nil
}

func (b *PTYBackend) waitLoop() {
	err := b.cmd.Wait()
	status := types.ExitStatus{Exited: true}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				if ws.Signaled() {
					status.Signal = SignalName(ws.Signal())
				} else {
					status.Code = ws.ExitStatus()
				}
			} else {
				status.Code = exitErr.ExitCode()
			}
		} else {
			status.Code = -1
		}
	}
	b.mu.Lock()
	b.status.State = "exited"
	b.status.Exit = status
	b.status.EndedAt = time.Now()
	b.mu.Unlock()
	close(b.statusCh)
}

func (b *PTYBackend) Read(p []byte) (int, error) {
	b.mu.Lock()
	master := b.master
	b.mu.Unlock()
	if master == nil {
		return 0, os.ErrClosed
	}
	return master.Read(p)
}

func (b *PTYBackend) Write(p []byte) (int, error) {
	b.mu.Lock()
	master := b.master
	b.mu.Unlock()
	if master == nil {
		return 0, os.ErrClosed
	}
	return master.Write(p)
}

func (b *PTYBackend) Wait(ctx context.Context) (types.ExitStatus, error) {
	select {
	case <-b.statusCh:
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.status.Exit, nil
	case <-ctx.Done():
		return types.ExitStatus{}, ctx.Err()
	}
}

func (b *PTYBackend) Status(ctx context.Context) (types.BackendStatus, error) {
	select {
	case <-b.statusCh:
	default:
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	status := b.status
	status.Caps = ptyCaps()
	return status, nil
}

func (b *PTYBackend) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("invalid_argument: cols and rows must be positive")
	}
	b.mu.Lock()
	master := b.master
	b.mu.Unlock()
	if master == nil {
		return os.ErrClosed
	}
	if err := pty.Setsize(master, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}); err != nil {
		return fmt.Errorf("resize_failed: %w", err)
	}
	return nil
}

func (b *PTYBackend) Signal(name string) error {
	sig, err := ParseSignal(name)
	if err != nil {
		return err
	}
	b.mu.Lock()
	master := b.master
	pid := 0
	if b.cmd != nil && b.cmd.Process != nil {
		pid = b.cmd.Process.Pid
	}
	b.mu.Unlock()
	if master == nil || pid == 0 {
		return os.ErrClosed
	}
	target := -pid
	if foreground, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPGRP); err == nil && foreground > 0 {
		target = -foreground
	}
	if err := syscall.Kill(target, sig); err != nil {
		return fmt.Errorf("signal_failed: %w", err)
	}
	return nil
}

func (b *PTYBackend) Close() error {
	var err error
	b.closeOne.Do(func() {
		b.mu.Lock()
		master := b.master
		pid := 0
		if b.cmd != nil && b.cmd.Process != nil {
			pid = b.cmd.Process.Pid
		}
		b.status.State = "closing"
		b.mu.Unlock()

		if pid > 0 {
			_ = syscall.Kill(-pid, syscall.SIGTERM)
			select {
			case <-b.statusCh:
			case <-time.After(200 * time.Millisecond):
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
		if master != nil {
			err = master.Close()
		}
		for _, dir := range b.cleanup {
			_ = os.RemoveAll(dir)
		}
	})
	return err
}

const shellBashRC = `__tt_precmd() {
  printf '\033]7;file://%s%s\007' "$(hostname)" "$PWD"
  printf '\033]133;A\007'
  printf '\033]133;B\007'
}
PROMPT_COMMAND="__tt_precmd;${PROMPT_COMMAND:-}"
`

const shellZshRC = `__tt_precmd() {
  printf '\033]7;file://%s%s\007' "$(hostname)" "$PWD"
  printf '\033]133;A\007'
  printf '\033]133;B\007'
}
autoload -Uz add-zsh-hook
add-zsh-hook precmd __tt_precmd
`

const shellFishRC = `function __tt_precmd --on-event fish_prompt
  printf '\033]7;file://%s%s\007' (hostname) $PWD
  printf '\033]133;A\007'
  printf '\033]133;B\007'
end
`

func shellIntegrationFiles(shell string) (string, string, error) {
	var source string
	switch shell {
	case "bash":
		source = shellBashRC
	case "zsh":
		source = shellZshRC
	case "fish":
		return "", "", nil
	case "sh", "dash", "ash", "ksh", "mksh":
		source = shellBashRC
	default:
		return "", "", nil
	}
	dir, err := os.MkdirTemp("", "tterminal-rc-")
	if err != nil {
		return "", "", fmt.Errorf("shell_integration: %w", err)
	}
	name := filepath.Join(dir, "rc")
	if err := os.WriteFile(name, []byte(source), 0o600); err != nil {
		os.RemoveAll(dir)
		return "", "", fmt.Errorf("shell_integration: %w", err)
	}
	return name, dir, nil
}

func BuildShellArgv(shell string, login bool, integration bool) []string {
	if shell == "" {
		shell = "/bin/sh"
	}
	args := []string{shell}
	if login {
		args = append(args, "-l")
	}
	args = append(args, "-i")
	_ = integration
	return args
}

func ShellName(shell string) string {
	return strings.TrimSpace(filepath.Base(shell))
}
