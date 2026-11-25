package session

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/uatx/tterminal/internal/backend"
	"github.com/uatx/tterminal/internal/config"
	"github.com/uatx/tterminal/internal/types"
)

type OpenRequest struct {
	Backend types.BackendKind `json:"backend"`
	PTY     PTYOpen           `json:"pty,omitempty"`
	Serial  SerialOpen        `json:"serial,omitempty"`
	Tmux    TmuxOpen          `json:"tmux,omitempty"`
	Cols    int               `json:"cols"`
	Rows    int               `json:"rows"`
}

type PTYOpen struct {
	Argv             []string `json:"argv,omitempty"`
	Shell            string   `json:"shell,omitempty"`
	Cwd              string   `json:"cwd,omitempty"`
	Env              []string `json:"env,omitempty"`
	Term             string   `json:"term,omitempty"`
	Login            bool     `json:"login,omitempty"`
	ShellIntegration bool     `json:"shell_integration,omitempty"`
}

type SerialOpen struct {
	Device        string `json:"device"`
	BaudRate      int    `json:"baud_rate,omitempty"`
	DataBits      int    `json:"data_bits,omitempty"`
	Parity        string `json:"parity,omitempty"`
	StopBits      string `json:"stop_bits,omitempty"`
	FlowControl   string `json:"flow_control,omitempty"`
	DTR           *bool  `json:"dtr,omitempty"`
	RTS           *bool  `json:"rts,omitempty"`
	ReadTimeoutMS int    `json:"read_timeout_ms,omitempty"`
}

type TmuxOpen struct {
	SocketName   string   `json:"socket_name,omitempty"`
	Session      string   `json:"session,omitempty"`
	Window       string   `json:"window,omitempty"`
	Pane         string   `json:"pane,omitempty"`
	Create       bool     `json:"create,omitempty"`
	Command      string   `json:"command,omitempty"`
	Cwd          string   `json:"cwd,omitempty"`
	Env          []string `json:"env,omitempty"`
	HistoryLimit int      `json:"history_limit,omitempty"`
	RemainOnExit bool     `json:"remain_on_exit,omitempty"`
}

type Manager struct {
	cfg      *config.Config
	mu       sync.RWMutex
	sessions map[string]*Session
	stopCh   chan struct{}
}

func NewManager(cfg *config.Config) *Manager {
	m := &Manager{cfg: cfg, sessions: map[string]*Session{}, stopCh: make(chan struct{})}
	go m.reapLoop()
	return m
}

func (m *Manager) reapLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.reap()
		case <-m.stopCh:
			return
		}
	}
}

func (m *Manager) reap() {
	ttl := time.Duration(m.cfg.Limits.SessionTTLMS) * time.Millisecond
	if ttl <= 0 {
		return
	}
	now := time.Now()
	m.mu.Lock()
	for id, session := range m.sessions {
		status, err := session.Status(context.Background())
		if err != nil || (status.State == "exited" || status.State == "closed") && !status.EndedAt.IsZero() && now.Sub(status.EndedAt) > ttl {
			session.Close()
			delete(m.sessions, id)
		}
	}
	m.mu.Unlock()
}

func (m *Manager) Open(ctx context.Context, req OpenRequest) (*Session, error) {
	m.mu.Lock()
	if len(m.sessions) >= m.cfg.Limits.MaxSessions {
		m.mu.Unlock()
		return nil, fmt.Errorf("limit_exceeded: maximum sessions reached")
	}
	m.mu.Unlock()

	if req.Cols <= 0 {
		req.Cols = m.cfg.Limits.DefaultCols
	}
	if req.Rows <= 0 {
		req.Rows = m.cfg.Limits.DefaultRows
	}

	var b backend.ByteStreamBackend
	meta := map[string]any{}

	switch req.Backend {
	case types.BackendPTY:
		open, err := m.preparePTY(req.PTY)
		if err != nil {
			return nil, err
		}
		b = backend.NewPTY(open)
		meta["argv"] = open.Argv
		meta["cwd"] = open.Cwd
		meta["term"] = open.Term
		meta["shell_integration"] = req.PTY.ShellIntegration
	case types.BackendSerial:
		if !m.cfg.IsDeviceAllowed(req.Serial.Device) {
			return nil, fmt.Errorf("permission_denied: serial device %q is not allowed", req.Serial.Device)
		}
		open := backend.SerialOptions{
			Device:      req.Serial.Device,
			BaudRate:    req.Serial.BaudRate,
			DataBits:    req.Serial.DataBits,
			Parity:      req.Serial.Parity,
			StopBits:    req.Serial.StopBits,
			FlowControl: req.Serial.FlowControl,
			DTR:         req.Serial.DTR,
			RTS:         req.Serial.RTS,
			ReadTimeout: time.Duration(req.Serial.ReadTimeoutMS) * time.Millisecond,
		}
		b = backend.NewSerial(open)
		meta["device"] = open.Device
		meta["baud_rate"] = open.BaudRate
		meta["data_bits"] = open.DataBits
		meta["parity"] = open.Parity
		meta["stop_bits"] = open.StopBits
		meta["flow_control"] = open.FlowControl
	case types.BackendTmux:
		if req.Tmux.Cwd != "" && !m.cfg.IsRootAllowed(req.Tmux.Cwd) {
			return nil, fmt.Errorf("permission_denied: cwd %q is not allowed", req.Tmux.Cwd)
		}
		if parts := strings.Fields(req.Tmux.Command); len(parts) > 0 {
			if _, err := exec.LookPath(parts[0]); err != nil {
				return nil, fmt.Errorf("invalid_argument: tmux command %q not found", parts[0])
			}
		}
		open := backend.TmuxOptions{
			SocketName:   req.Tmux.SocketName,
			Session:      req.Tmux.Session,
			Window:       req.Tmux.Window,
			Pane:         req.Tmux.Pane,
			Create:       req.Tmux.Create,
			Command:      req.Tmux.Command,
			Cwd:          req.Tmux.Cwd,
			Env:          m.filterEnv(req.Tmux.Env),
			HistoryLimit: req.Tmux.HistoryLimit,
			RemainOnExit: req.Tmux.RemainOnExit,
			Cols:         req.Cols,
			Rows:         req.Rows,
		}
		b = backend.NewTmux(open)
		meta["tmux_socket"] = open.SocketName
		meta["tmux_session"] = open.Session
		meta["command"] = open.Command
		meta["cwd"] = open.Cwd
	default:
		return nil, fmt.Errorf("invalid_argument: unknown backend %q", req.Backend)
	}

	s := newSession(randomID(12), req.Backend, m.cfg, b, req.Cols, req.Rows, meta)
	if err := s.Start(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.sessions[s.ID()] = s
	m.mu.Unlock()
	return s, nil
}

func (m *Manager) preparePTY(open PTYOpen) (backend.PTYOptions, error) {
	result := backend.PTYOptions{
		Argv:             append([]string(nil), open.Argv...),
		Shell:            open.Shell,
		Cwd:              open.Cwd,
		Env:              m.filterEnv(open.Env),
		Term:             open.Term,
		Login:            open.Login,
		ShellIntegration: open.ShellIntegration && m.cfg.Security.AllowShellIntegration,
	}
	if len(result.Argv) == 0 {
		if result.Shell == "" {
			result.Shell = "/bin/sh"
		}
		result.Argv = backend.BuildShellArgv(result.Shell, result.Login, result.ShellIntegration)
	}
	resolved, err := exec.LookPath(result.Argv[0])
	if err != nil {
		return result, fmt.Errorf("invalid_argument: executable %q not found", result.Argv[0])
	}
	result.Argv[0] = resolved
	if !m.cfg.IsExecutableAllowed(resolved) {
		return result, fmt.Errorf("permission_denied: executable %q is not allowed", resolved)
	}
	if result.Cwd != "" {
		if !m.cfg.IsRootAllowed(result.Cwd) {
			return result, fmt.Errorf("permission_denied: cwd %q is not allowed", result.Cwd)
		}
	}
	return result, nil
}

func (m *Manager) filterEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, item := range env {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 || !m.cfg.IsEnvAllowed(parts[0]) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	session, ok := m.sessions[id]
	return session, ok
}

func (m *Manager) Require(id string) (*Session, error) {
	session, ok := m.Get(id)
	if !ok {
		return nil, fmt.Errorf("session_not_found: %s", id)
	}
	return session, nil
}

func (m *Manager) List(ctx context.Context) ([]Info, error) {
	m.mu.RLock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.RUnlock()

	out := make([]Info, 0, len(sessions))
	for _, session := range sessions {
		info, err := session.Info(ctx)
		if err != nil {
			continue
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Manager) Close(id string) error {
	session, err := m.Require(id)
	if err != nil {
		return err
	}
	err = session.Close()
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
	return err
}

func (m *Manager) Shutdown() error {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.sessions = map[string]*Session{}
	m.mu.Unlock()
	close(m.stopCh)
	for _, session := range sessions {
		_ = session.Close()
	}
	return nil
}
