package backend

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/uatx/tterminal/internal/types"
)

type TmuxOptions struct {
	SocketName   string
	Session      string
	Window       string
	Pane         string
	Create       bool
	Command      string
	Cwd          string
	Env          []string
	HistoryLimit int
	RemainOnExit bool
	Cols         int
	Rows         int
}

type TmuxBackend struct {
	opts    TmuxOptions
	target  string
	output  *io.PipeReader
	outputW *io.PipeWriter

	mu          sync.Mutex
	status      types.BackendStatus
	doneCh      chan struct{}
	closeOne    sync.Once
	stopOne     sync.Once
	pollStop    chan struct{}
	lastCapture string
}

func NewTmux(opts TmuxOptions) *TmuxBackend {
	if opts.Cols <= 0 {
		opts.Cols = 80
	}
	if opts.Rows <= 0 {
		opts.Rows = 24
	}
	if opts.HistoryLimit <= 0 {
		opts.HistoryLimit = 10000
	}
	if opts.SocketName == "" {
		opts.SocketName = fmt.Sprintf("tterminal-%d", os.Getuid())
	}
	reader, writer := io.Pipe()
	return &TmuxBackend{
		opts:     opts,
		target:   opts.Session,
		output:   reader,
		outputW:  writer,
		doneCh:   make(chan struct{}),
		pollStop: make(chan struct{}),
		status: types.BackendStatus{
			State:     "initializing",
			Caps:      tmuxCaps(),
			StartedAt: time.Now(),
		},
	}
}

func tmuxCaps() types.BackendCaps {
	return types.BackendCaps{CanSignal: true, CanResize: true, CanReportExit: true}
}

func (b *TmuxBackend) Kind() types.BackendKind         { return types.BackendTmux }
func (b *TmuxBackend) Capabilities() types.BackendCaps { return tmuxCaps() }

func (b *TmuxBackend) tmuxArgs(args ...string) []string {
	return append([]string{"-L", b.opts.SocketName, "-f", "/dev/null"}, args...)
}

func (b *TmuxBackend) run(args ...string) (string, error) {
	out, err := exec.Command("tmux", b.tmuxArgs(args...)...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("tmux_%s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (b *TmuxBackend) Start(ctx context.Context) error {
	if b.opts.Session == "" {
		return fmt.Errorf("invalid_argument: session is required for tmux backend")
	}
	if b.opts.Create {
		args := []string{"new-session", "-d", "-s", b.opts.Session, "-x", strconv.Itoa(b.opts.Cols), "-y", strconv.Itoa(b.opts.Rows)}
		if b.opts.Cwd != "" {
			args = append(args, "-c", b.opts.Cwd)
		}
		if b.opts.Command != "" {
			args = append(args, "--", b.opts.Command)
		}
		if _, err := b.run(args...); err != nil {
			return err
		}
		remain := "off"
		if b.opts.RemainOnExit {
			remain = "on"
		}
		if _, err := b.run("set-option", "-t", b.opts.Session, "remain-on-exit", remain); err != nil {
			return err
		}
		if _, err := b.run("set-option", "-t", b.opts.Session, "status", "off"); err != nil {
			return err
		}
		if _, err := b.run("set-option", "-t", b.opts.Session, "history-limit", strconv.Itoa(b.opts.HistoryLimit)); err != nil {
			return err
		}
		for _, env := range b.opts.Env {
			parts := strings.SplitN(env, "=", 2)
			if len(parts) == 2 {
				if _, err := b.run("set-environment", "-t", b.opts.Session, parts[0], parts[1]); err != nil {
					return err
				}
			}
		}
	}

	b.target = b.opts.Session
	if b.opts.Window != "" {
		b.target += ":" + b.opts.Window
	}
	if b.opts.Pane != "" {
		b.target += "." + b.opts.Pane
	}
	if b.opts.Window == "" && b.opts.Pane == "" {
		targetOutput, err := b.run("display-message", "-p", "-t", b.opts.Session, "#{session_name}:#{window_index}.#{pane_index}")
		if err != nil {
			return err
		}
		b.target = strings.TrimSpace(targetOutput)
	}

	b.mu.Lock()
	b.status.State = "running"
	b.status.Detail = b.target
	b.mu.Unlock()

	go b.pollLoop()
	return nil
}

func (b *TmuxBackend) pollLoop() {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-b.pollStop:
			return
		case <-ticker.C:
		}
		text, err := b.run("capture-pane", "-p", "-J", "-t", b.target, "-S", "-", "-E", "-")
		if err != nil {
			continue
		}
		b.mu.Lock()
		previous := b.lastCapture
		b.lastCapture = text
		b.mu.Unlock()
		if suffix := captureSuffix(previous, text); suffix != "" {
			if _, err := b.outputW.Write([]byte(suffix)); err != nil {
				return
			}
		}
		if b.paneDead() {
			b.markDead()
			return
		}
	}
}

func captureSuffix(previous, current string) string {
	if previous == current {
		return ""
	}
	if strings.HasPrefix(current, previous) {
		return current[len(previous):]
	}
	common := 0
	for common < len(previous) && common < len(current) && previous[common] == current[common] {
		common++
	}
	return current[common:]
}

func (b *TmuxBackend) paneDead() bool {
	out, err := b.run("display-message", "-p", "-t", b.target, "#{pane_dead}")
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == "1"
}

func (b *TmuxBackend) paneExitCode() int {
	out, err := b.run("display-message", "-p", "-t", b.target, "#{pane_dead_status}")
	if err != nil {
		return -1
	}
	code, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return -1
	}
	return code
}

func (b *TmuxBackend) markDead() {
	b.mu.Lock()
	if b.status.State == "exited" || b.status.State == "closed" {
		b.mu.Unlock()
		return
	}
	b.status.State = "exited"
	b.status.Exit = types.ExitStatus{Exited: true, Code: b.paneExitCode()}
	b.status.EndedAt = time.Now()
	b.mu.Unlock()
	b.outputW.Close()
	select {
	case <-b.doneCh:
	default:
		close(b.doneCh)
	}
}

func (b *TmuxBackend) Read(p []byte) (int, error) { return b.output.Read(p) }

func (b *TmuxBackend) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if _, err := b.run("send-keys", "-l", "-t", b.target, "--", string(p)); err != nil {
		return 0, err
	}
	if strings.ContainsAny(string(p), "\r\n") {
		if _, err := b.run("send-keys", "-t", b.target, "Enter"); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (b *TmuxBackend) Wait(ctx context.Context) (types.ExitStatus, error) {
	select {
	case <-b.doneCh:
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.status.Exit, nil
	case <-ctx.Done():
		return types.ExitStatus{}, ctx.Err()
	}
}

func (b *TmuxBackend) Status(ctx context.Context) (types.BackendStatus, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	status := b.status
	status.Caps = tmuxCaps()
	return status, nil
}

func (b *TmuxBackend) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("invalid_argument: cols and rows must be positive")
	}
	if _, err := b.run("resize-pane", "-t", b.target, "-x", strconv.Itoa(cols), "-y", strconv.Itoa(rows)); err != nil {
		return err
	}
	b.mu.Lock()
	b.opts.Cols = cols
	b.opts.Rows = rows
	b.status.Detail = fmt.Sprintf("%s %dx%d", b.target, cols, rows)
	b.mu.Unlock()
	return nil
}

func (b *TmuxBackend) Signal(name string) error {
	key := ""
	switch strings.ToUpper(strings.TrimPrefix(name, "SIG")) {
	case "INT":
		key = "C-C"
	case "QUIT":
		key = `C-\`
	case "TSTP":
		key = "C-Z"
	case "WINCH":
		b.mu.Lock()
		cols, rows := b.opts.Cols, b.opts.Rows
		b.mu.Unlock()
		return b.Resize(cols, rows)
	default:
		return Unsupported("signal", types.BackendTmux)
	}
	if _, err := b.run("send-keys", "-t", b.target, key); err != nil {
		return err
	}
	return nil
}

func (b *TmuxBackend) Control(ctx context.Context, req ControlRequest) (map[string]any, error) {
	switch req.Action {
	case "capture_pane":
		out, err := b.run("capture-pane", "-p", "-J", "-t", b.target)
		if err != nil {
			return nil, err
		}
		return map[string]any{"text": out}, nil
	case "clear":
		if _, err := b.run("clear-history", "-t", b.target); err != nil {
			return nil, err
		}
	case "renumber_windows":
		if _, err := b.run("renumber-windows", "-t", b.opts.Session); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("invalid_argument: unknown tmux control action %q", req.Action)
	}
	return map[string]any{"ok": true}, nil
}

func (b *TmuxBackend) Close() error {
	var err error
	b.closeOne.Do(func() {
		b.stopOne.Do(func() { close(b.pollStop) })
		_, _ = b.run("kill-session", "-t", b.opts.Session)
		b.mu.Lock()
		b.status.State = "closed"
		b.status.EndedAt = time.Now()
		b.mu.Unlock()
		b.outputW.Close()
		select {
		case <-b.doneCh:
		default:
			close(b.doneCh)
		}
		_ = err
	})
	return err
}
