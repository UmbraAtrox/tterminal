package daemon

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/uatx/tterminal/internal/backend"
	"github.com/uatx/tterminal/internal/buffer"
	"github.com/uatx/tterminal/internal/config"
	"github.com/uatx/tterminal/internal/screen"
	"github.com/uatx/tterminal/internal/session"
	"github.com/uatx/tterminal/internal/types"
)

type openIn struct {
	Backend      types.BackendKind `json:"backend" jsonschema:"backend to create: pty, serial, or tmux"`
	PTY          openPTYIn         `json:"pty,omitempty" jsonschema:"native PTY options"`
	Serial       openSerialIn      `json:"serial,omitempty" jsonschema:"serial port options"`
	Tmux         openTmuxIn        `json:"tmux,omitempty" jsonschema:"tmux backend options"`
	Cols         int               `json:"cols,omitempty" jsonschema:"terminal columns"`
	Rows         int               `json:"rows,omitempty" jsonschema:"terminal rows"`
	OutputPolicy openOutputPolicy  `json:"output_policy,omitempty" jsonschema:"default output window for this session"`
}

type openOutputPolicy struct {
	Mode     string `json:"mode,omitempty" jsonschema:"tail, delta, or all"`
	MaxBytes int    `json:"max_bytes,omitempty" jsonschema:"maximum bytes for tail output"`
	MaxLines int    `json:"max_lines,omitempty" jsonschema:"maximum lines for tail output"`
}

type openPTYIn struct {
	Argv             []string `json:"argv,omitempty" jsonschema:"command and arguments"`
	Shell            string   `json:"shell,omitempty" jsonschema:"shell path when argv is omitted"`
	Cwd              string   `json:"cwd,omitempty" jsonschema:"working directory"`
	Env              []string `json:"env,omitempty" jsonschema:"environment as KEY=value"`
	Term             string   `json:"term,omitempty" jsonschema:"TERM value"`
	Login            bool     `json:"login,omitempty" jsonschema:"start a login shell"`
	ShellIntegration *bool    `json:"shell_integration,omitempty" jsonschema:"inject shell integration markers"`
}

type openSerialIn struct {
	Device        string `json:"device" jsonschema:"serial device path"`
	BaudRate      int    `json:"baud_rate,omitempty" jsonschema:"bits per second"`
	DataBits      int    `json:"data_bits,omitempty" jsonschema:"data bits"`
	Parity        string `json:"parity,omitempty" jsonschema:"none, even, odd, mark, or space"`
	StopBits      string `json:"stop_bits,omitempty" jsonschema:"1, 1.5, or 2"`
	FlowControl   string `json:"flow_control,omitempty" jsonschema:"none, rtscts, or xonxoff"`
	DTR           *bool  `json:"dtr,omitempty" jsonschema:"initial DTR line state"`
	RTS           *bool  `json:"rts,omitempty" jsonschema:"initial RTS line state"`
	ReadTimeoutMS int    `json:"read_timeout_ms,omitempty" jsonschema:"serial read timeout in milliseconds"`
}

type openTmuxIn struct {
	SocketName   string   `json:"socket_name,omitempty" jsonschema:"tmux socket name"`
	Session      string   `json:"session" jsonschema:"tmux session name"`
	Window       string   `json:"window,omitempty" jsonschema:"window selector"`
	Pane         string   `json:"pane,omitempty" jsonschema:"pane selector"`
	Create       bool     `json:"create,omitempty" jsonschema:"create the session"`
	Command      string   `json:"command,omitempty" jsonschema:"initial shell command"`
	Cwd          string   `json:"cwd,omitempty" jsonschema:"working directory"`
	Env          []string `json:"env,omitempty" jsonschema:"environment as KEY=value"`
	HistoryLimit int      `json:"history_limit,omitempty" jsonschema:"tmux history limit"`
	RemainOnExit *bool    `json:"remain_on_exit,omitempty" jsonschema:"keep pane after process exits"`
}

type openOut struct {
	ID      string            `json:"id"`
	Backend types.BackendKind `json:"backend"`
	Info    session.Info      `json:"info"`
}

type sessionIn struct {
	SessionID string `json:"session_id" jsonschema:"session identifier"`
}

type writeIn struct {
	SessionID string `json:"session_id" jsonschema:"session identifier"`
	Data      string `json:"data" jsonschema:"data to write"`
	Encoding  string `json:"encoding,omitempty" jsonschema:"utf8, base64, or hex"`
	Enter     bool   `json:"enter,omitempty" jsonschema:"append carriage return"`
}

type writeOut struct {
	SessionID    string `json:"session_id"`
	BytesWritten int    `json:"bytes_written"`
	Cursor       int64  `json:"cursor"`
}

type outputAccounting struct {
	RangeStartCursor int64  `json:"range_start_cursor"`
	Cursor           int64  `json:"cursor"`
	NextCursor       int64  `json:"next_cursor"`
	BaseCursor       int64  `json:"base_cursor"`
	AvailableBytes   int64  `json:"available_bytes"`
	SkippedBytes     int64  `json:"skipped_bytes"`
	RingDroppedBytes int64  `json:"ring_dropped_bytes"`
	Truncated        bool   `json:"truncated"`
	TruncationReason string `json:"truncation_reason,omitempty"`
}

type readIn struct {
	SessionID string `json:"session_id" jsonschema:"session identifier"`
	Cursor    int64  `json:"cursor,omitempty" jsonschema:"byte cursor"`
	Mode      string `json:"mode,omitempty" jsonschema:"tail, delta, or all"`
	MaxBytes  int    `json:"max_bytes,omitempty" jsonschema:"maximum bytes for tail output"`
	MaxLines  int    `json:"max_lines,omitempty" jsonschema:"maximum lines for tail output"`
	Format    string `json:"format,omitempty" jsonschema:"raw, text, hex, screen, or both"`
	Encoding  string `json:"encoding,omitempty" jsonschema:"utf8, base64, or hex"`
	WaitMS    int    `json:"wait_ms,omitempty" jsonschema:"wait up to this many milliseconds for data"`
}

type readOut struct {
	SessionID string           `json:"session_id"`
	Data      string           `json:"data,omitempty"`
	Encoding  string           `json:"encoding,omitempty"`
	Screen    *screen.Snapshot `json:"screen,omitempty"`
	outputAccounting
}

type waitIn struct {
	SessionID string `json:"session_id" jsonschema:"session identifier"`
	Until     string `json:"until,omitempty" jsonschema:"data, exit, regex, quiet, or marker"`
	Pattern   string `json:"pattern,omitempty" jsonschema:"regular expression for until=regex"`
	QuietMS   int    `json:"quiet_ms,omitempty" jsonschema:"quiet period in milliseconds"`
	MarkerID  string `json:"marker_id,omitempty" jsonschema:"marker identifier for until=marker"`
	Cursor    int64  `json:"cursor,omitempty" jsonschema:"byte cursor"`
	Mode      string `json:"mode,omitempty" jsonschema:"tail, delta, or all"`
	TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"timeout in milliseconds"`
	MaxBytes  int    `json:"max_bytes,omitempty" jsonschema:"maximum bytes for tail output"`
	MaxLines  int    `json:"max_lines,omitempty" jsonschema:"maximum lines for tail output"`
	Format    string `json:"format,omitempty" jsonschema:"raw, text, hex, screen, or both"`
	Encoding  string `json:"encoding,omitempty" jsonschema:"utf8, base64, or hex"`
}

type waitOut struct {
	SessionID string           `json:"session_id"`
	Reason    string           `json:"reason"`
	TimedOut  bool             `json:"timed_out"`
	Data      string           `json:"data,omitempty"`
	Encoding  string           `json:"encoding,omitempty"`
	Screen    *screen.Snapshot `json:"screen,omitempty"`
	Matches   []string         `json:"matches,omitempty"`
	Exit      types.ExitStatus `json:"exit,omitempty"`
	outputAccounting
}

type execIn struct {
	SessionID string `json:"session_id" jsonschema:"session identifier"`
	Command   string `json:"command" jsonschema:"shell command to execute in the session"`
	Cursor    int64  `json:"cursor,omitempty" jsonschema:"byte cursor for delta output"`
	Mode      string `json:"mode,omitempty" jsonschema:"tail, delta, or all"`
	TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"timeout in milliseconds"`
	MaxBytes  int    `json:"max_bytes,omitempty" jsonschema:"maximum bytes for tail output"`
	MaxLines  int    `json:"max_lines,omitempty" jsonschema:"maximum lines for tail output"`
	Format    string `json:"format,omitempty" jsonschema:"raw, text, hex, screen, or both"`
	Encoding  string `json:"encoding,omitempty" jsonschema:"utf8, base64, or hex"`
}

type execOut struct {
	SessionID string           `json:"session_id"`
	ID        string           `json:"id"`
	Exit      types.ExitStatus `json:"exit"`
	TimedOut  bool             `json:"timed_out"`
	Data      string           `json:"data,omitempty"`
	Encoding  string           `json:"encoding,omitempty"`
	Screen    *screen.Snapshot `json:"screen,omitempty"`
	outputAccounting
}

type screenOut struct {
	SessionID string          `json:"session_id"`
	Info      session.Info    `json:"info"`
	Screen    screen.Snapshot `json:"screen"`
}

type signalIn struct {
	SessionID string `json:"session_id" jsonschema:"session identifier"`
	Signal    string `json:"signal" jsonschema:"signal name such as INT, TERM, or BRK"`
}

type controlIn struct {
	SessionID string         `json:"session_id" jsonschema:"session identifier"`
	Action    string         `json:"action" jsonschema:"control action"`
	Value     map[string]any `json:"value,omitempty" jsonschema:"action parameters"`
}

type resizeIn struct {
	SessionID string `json:"session_id" jsonschema:"session identifier"`
	Cols      int    `json:"cols" jsonschema:"terminal columns"`
	Rows      int    `json:"rows" jsonschema:"terminal rows"`
}

type infoOut struct {
	Info session.Info `json:"info"`
}

type listOut struct {
	Sessions []session.Info `json:"sessions"`
}

type closeOut struct {
	SessionID string           `json:"session_id"`
	Exit      types.ExitStatus `json:"exit"`
	Info      session.Info     `json:"info"`
}

type devicesOut struct {
	Devices []string `json:"devices"`
}

type okOut struct {
	OK bool `json:"ok"`
}

func newMCPServer(s *Server) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "tterminal", Version: "0.1.0"}, &mcp.ServerOptions{
		Instructions:              "Persistent PTY, serial, and tmux sessions for agents.",
		Logger:                    s.logger,
		Capabilities:              &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: false}},
		SupportedProtocolVersions: []string{"2026-07-28", "2025-11-25"},
	})
	mcp.AddTool(server, &mcp.Tool{Name: "term_open", Description: "Open a persistent terminal or serial session"}, s.termOpen)
	mcp.AddTool(server, &mcp.Tool{Name: "term_write", Description: "Write bytes to a session"}, s.termWrite)
	mcp.AddTool(server, &mcp.Tool{Name: "term_read", Description: "Read session output using a byte cursor"}, s.termRead)
	mcp.AddTool(server, &mcp.Tool{Name: "term_wait", Description: "Wait for output, exit, quiet, regex, or marker"}, s.termWait)
	mcp.AddTool(server, &mcp.Tool{Name: "term_exec", Description: "Run a shell command inside an existing session"}, s.termExec)
	mcp.AddTool(server, &mcp.Tool{Name: "term_screen", Description: "Read the rendered terminal screen"}, s.termScreen)
	mcp.AddTool(server, &mcp.Tool{Name: "term_signal", Description: "Send a signal to a session"}, s.termSignal)
	mcp.AddTool(server, &mcp.Tool{Name: "term_control", Description: "Run a backend or session control action"}, s.termControl)
	mcp.AddTool(server, &mcp.Tool{Name: "term_resize", Description: "Resize a session"}, s.termResize)
	mcp.AddTool(server, &mcp.Tool{Name: "term_close", Description: "Close and remove a session"}, s.termClose)
	mcp.AddTool(server, &mcp.Tool{Name: "term_list", Description: "List sessions"}, s.termList)
	mcp.AddTool(server, &mcp.Tool{Name: "term_info", Description: "Describe a session"}, s.termInfo)
	mcp.AddTool(server, &mcp.Tool{Name: "term_devices", Description: "List available serial devices"}, s.termDevices)
	return server
}

func (s *Server) identityFor(req *mcp.CallToolRequest) types.Identity {
	if req != nil && req.Session != nil {
		if identity, ok := s.identity(req.Session.ID()); ok {
			return identity
		}
	}
	return types.Identity{UID: os.Getuid(), GID: os.Getgid(), PID: os.Getpid()}
}

func (s *Server) requireRole(req *mcp.CallToolRequest, role types.Role) (types.Identity, error) {
	identity := s.identityFor(req)
	actual := s.cfg.RoleFor(identity.UID)
	if actual.Rank() < role.Rank() {
		return identity, fmt.Errorf("permission_denied: %s role required, have %s", role, actual)
	}
	return identity, nil
}

func (s *Server) requireSession(req *mcp.CallToolRequest, id string, role types.Role) (*session.Session, types.Identity, error) {
	identity, err := s.requireRole(req, role)
	if err != nil {
		return nil, identity, err
	}
	target, err := s.manager.Require(id)
	if err != nil {
		return nil, identity, err
	}
	owner, ok := asIntValue(target.Meta("owner_uid"))
	if ok && owner != identity.UID && identity.UID != 0 && s.cfg.RoleFor(identity.UID).Rank() < types.RoleController.Rank() {
		return nil, identity, fmt.Errorf("permission_denied: session %s belongs to another user", id)
	}
	return target, identity, nil
}

func asIntValue(value any, ok bool) (int, bool) {
	if !ok {
		return 0, false
	}
	return asInt(value)
}

func asInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func accountFor(window buffer.WindowResult) outputAccounting {
	return outputAccounting{
		RangeStartCursor: window.RangeStart,
		Cursor:           window.Cursor,
		NextCursor:       window.Next,
		BaseCursor:       window.Base,
		AvailableBytes:   window.AvailableBytes,
		SkippedBytes:     window.SkippedBytes,
		RingDroppedBytes: window.RingDroppedBytes,
		Truncated:        window.Truncated,
		TruncationReason: window.TruncationReason,
	}
}

func screenSnapshot(target *session.Session, cfg *config.Config) screen.Snapshot {
	return target.Screen().SnapshotCapped(cfg.Limits.MaxScreenChars)
}

func (s *Server) termOpen(ctx context.Context, req *mcp.CallToolRequest, in openIn) (*mcp.CallToolResult, openOut, error) {
	identity, err := s.requireRole(req, types.RoleWriter)
	if err != nil {
		return nil, openOut{}, err
	}
	if in.Backend == "" {
		in.Backend = types.BackendPTY
	}
	integration := true
	if in.PTY.ShellIntegration != nil {
		integration = *in.PTY.ShellIntegration
	}
	remain := true
	if in.Tmux.RemainOnExit != nil {
		remain = *in.Tmux.RemainOnExit
	}
	openReq := session.OpenRequest{
		Backend: in.Backend,
		Cols:    in.Cols,
		Rows:    in.Rows,
		OutputPolicy: session.OutputPolicy{
			Mode:     in.OutputPolicy.Mode,
			MaxBytes: in.OutputPolicy.MaxBytes,
			MaxLines: in.OutputPolicy.MaxLines,
		},
		PTY: session.PTYOpen{
			Argv: in.PTY.Argv, Shell: in.PTY.Shell, Cwd: in.PTY.Cwd, Env: in.PTY.Env,
			Term: in.PTY.Term, Login: in.PTY.Login, ShellIntegration: integration,
		},
		Serial: session.SerialOpen{
			Device: in.Serial.Device, BaudRate: in.Serial.BaudRate, DataBits: in.Serial.DataBits,
			Parity: in.Serial.Parity, StopBits: in.Serial.StopBits, FlowControl: in.Serial.FlowControl,
			DTR: in.Serial.DTR, RTS: in.Serial.RTS, ReadTimeoutMS: in.Serial.ReadTimeoutMS,
		},
		Tmux: session.TmuxOpen{
			SocketName: in.Tmux.SocketName, Session: in.Tmux.Session, Window: in.Tmux.Window,
			Pane: in.Tmux.Pane, Create: in.Tmux.Create, Command: in.Tmux.Command, Cwd: in.Tmux.Cwd,
			Env: in.Tmux.Env, HistoryLimit: in.Tmux.HistoryLimit, RemainOnExit: remain,
		},
	}
	created, err := s.manager.Open(ctx, openReq)
	if err != nil {
		return nil, openOut{}, err
	}
	created.SetMeta("owner_uid", identity.UID)
	info, err := created.Info(ctx)
	if err != nil {
		return nil, openOut{}, err
	}
	return nil, openOut{ID: created.ID(), Backend: created.Kind(), Info: info}, nil
}

func (s *Server) termWrite(ctx context.Context, req *mcp.CallToolRequest, in writeIn) (*mcp.CallToolResult, writeOut, error) {
	target, _, err := s.requireSession(req, in.SessionID, types.RoleWriter)
	if err != nil {
		return nil, writeOut{}, err
	}
	data, err := decodeInput([]byte(in.Data), in.Encoding)
	if err != nil {
		return nil, writeOut{}, err
	}
	if in.Enter {
		data = append(data, '\r')
	}
	written, err := target.Write(data)
	if err != nil {
		return nil, writeOut{}, err
	}
	_, cursor := target.Ring().Range()
	return nil, writeOut{SessionID: in.SessionID, BytesWritten: written, Cursor: cursor}, nil
}

func (s *Server) termRead(ctx context.Context, req *mcp.CallToolRequest, in readIn) (*mcp.CallToolResult, readOut, error) {
	target, _, err := s.requireSession(req, in.SessionID, types.RoleReader)
	if err != nil {
		return nil, readOut{}, err
	}
	requested := in.Cursor
	if requested <= 0 {
		base, _ := target.Ring().Range()
		requested = base
	}
	if in.WaitMS > 0 {
		waitResult, err := target.Wait(ctx, session.WaitRequest{Until: "data", Cursor: requested, TimeoutMS: in.WaitMS, MaxBytes: s.cfg.Limits.MaxRingBytes})
		if err != nil {
			return nil, readOut{}, err
		}
		if waitResult.TimedOut {
			base, next := target.Ring().Range()
			retained := requested
			if retained < base {
				retained = base
			}
			if retained > next {
				retained = next
			}
			dropped := base - requested
			if dropped < 0 {
				dropped = 0
			}
			return nil, readOut{
				SessionID: in.SessionID,
				outputAccounting: outputAccounting{
					RangeStartCursor: requested, Cursor: retained, NextCursor: next, BaseCursor: base,
					AvailableBytes: next - retained, RingDroppedBytes: dropped, Truncated: dropped > 0,
				},
			}, nil
		}
	}
	window, err := target.Window(requested, 0, in.Mode, in.MaxBytes, in.MaxLines)
	if err != nil {
		return nil, readOut{}, err
	}
	formatted, encoding := formatData(window.Data, in.Format, in.Encoding)
	out := readOut{
		SessionID: in.SessionID, Data: formatted, Encoding: encoding,
		outputAccounting: accountFor(window),
	}
	if strings.EqualFold(in.Format, "screen") || strings.EqualFold(in.Format, "both") {
		snapshot := screenSnapshot(target, s.cfg)
		out.Screen = &snapshot
	}
	return nil, out, nil
}

func (s *Server) termWait(ctx context.Context, req *mcp.CallToolRequest, in waitIn) (*mcp.CallToolResult, waitOut, error) {
	target, _, err := s.requireSession(req, in.SessionID, types.RoleReader)
	if err != nil {
		return nil, waitOut{}, err
	}
	requested := in.Cursor
	if requested <= 0 {
		base, _ := target.Ring().Range()
		requested = base
	}
	result, err := target.Wait(ctx, session.WaitRequest{
		Until: in.Until, Pattern: in.Pattern, QuietMS: in.QuietMS, MarkerID: in.MarkerID,
		Cursor: requested, TimeoutMS: in.TimeoutMS, MaxBytes: s.cfg.Limits.MaxRingBytes,
	})
	if err != nil {
		return nil, waitOut{}, err
	}
	window, err := target.Window(result.RangeStart, result.NextCursor, in.Mode, in.MaxBytes, in.MaxLines)
	if err != nil {
		return nil, waitOut{}, err
	}
	formatted, encoding := formatData(window.Data, in.Format, in.Encoding)
	out := waitOut{
		SessionID: in.SessionID, Reason: result.Reason, TimedOut: result.TimedOut,
		Data: formatted, Encoding: encoding, Matches: result.Matches, Exit: result.Exit,
		outputAccounting: accountFor(window),
	}
	if strings.EqualFold(in.Format, "screen") || strings.EqualFold(in.Format, "both") {
		snapshot := screenSnapshot(target, s.cfg)
		out.Screen = &snapshot
	}
	return nil, out, nil
}

func (s *Server) termExec(ctx context.Context, req *mcp.CallToolRequest, in execIn) (*mcp.CallToolResult, execOut, error) {
	target, _, err := s.requireSession(req, in.SessionID, types.RoleWriter)
	if err != nil {
		return nil, execOut{}, err
	}
	result, err := target.Exec(ctx, in.Command, in.TimeoutMS)
	if err != nil {
		return nil, execOut{}, err
	}
	requested := result.RangeStart
	if in.Cursor > 0 {
		requested = in.Cursor
	}
	window, err := target.Window(requested, result.NextCursor, in.Mode, in.MaxBytes, in.MaxLines)
	if err != nil {
		return nil, execOut{}, err
	}
	formatted, encoding := formatData(window.Data, in.Format, in.Encoding)
	out := execOut{
		SessionID: in.SessionID, ID: result.ID, Exit: result.Exit, TimedOut: result.TimedOut,
		Data: formatted, Encoding: encoding, outputAccounting: accountFor(window),
	}
	if strings.EqualFold(in.Format, "screen") || strings.EqualFold(in.Format, "both") {
		snapshot := screenSnapshot(target, s.cfg)
		out.Screen = &snapshot
	}
	return nil, out, nil
}

func (s *Server) termScreen(ctx context.Context, req *mcp.CallToolRequest, in sessionIn) (*mcp.CallToolResult, screenOut, error) {
	target, _, err := s.requireSession(req, in.SessionID, types.RoleReader)
	if err != nil {
		return nil, screenOut{}, err
	}
	info, err := target.Info(ctx)
	if err != nil {
		return nil, screenOut{}, err
	}
	return nil, screenOut{SessionID: in.SessionID, Info: info, Screen: screenSnapshot(target, s.cfg)}, nil
}

func (s *Server) termSignal(ctx context.Context, req *mcp.CallToolRequest, in signalIn) (*mcp.CallToolResult, okOut, error) {
	target, _, err := s.requireSession(req, in.SessionID, types.RoleController)
	if err != nil {
		return nil, okOut{}, err
	}
	if err := target.Signal(in.Signal); err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{OK: true}, nil
}

func (s *Server) termControl(ctx context.Context, req *mcp.CallToolRequest, in controlIn) (*mcp.CallToolResult, map[string]any, error) {
	target, _, err := s.requireSession(req, in.SessionID, types.RoleController)
	if err != nil {
		return nil, nil, err
	}
	result, err := target.Control(ctx, in.Action, in.Value)
	if err != nil {
		return nil, nil, err
	}
	return nil, result, nil
}

func (s *Server) termResize(ctx context.Context, req *mcp.CallToolRequest, in resizeIn) (*mcp.CallToolResult, okOut, error) {
	target, _, err := s.requireSession(req, in.SessionID, types.RoleController)
	if err != nil {
		return nil, okOut{}, err
	}
	if err := target.Resize(in.Cols, in.Rows); err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{OK: true}, nil
}

func (s *Server) termClose(ctx context.Context, req *mcp.CallToolRequest, in sessionIn) (*mcp.CallToolResult, closeOut, error) {
	target, _, err := s.requireSession(req, in.SessionID, types.RoleController)
	if err != nil {
		return nil, closeOut{}, err
	}
	info, _ := target.Info(ctx)
	if err := s.manager.Close(in.SessionID); err != nil {
		return nil, closeOut{}, err
	}
	status, _ := target.Status(ctx)
	return nil, closeOut{SessionID: in.SessionID, Exit: status.Exit, Info: info}, nil
}

func (s *Server) termList(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listOut, error) {
	if _, err := s.requireRole(req, types.RoleReader); err != nil {
		return nil, listOut{}, err
	}
	sessions, err := s.manager.List(ctx)
	if err != nil {
		return nil, listOut{}, err
	}
	return nil, listOut{Sessions: sessions}, nil
}

func (s *Server) termInfo(ctx context.Context, req *mcp.CallToolRequest, in sessionIn) (*mcp.CallToolResult, infoOut, error) {
	target, _, err := s.requireSession(req, in.SessionID, types.RoleReader)
	if err != nil {
		return nil, infoOut{}, err
	}
	info, err := target.Info(ctx)
	if err != nil {
		return nil, infoOut{}, err
	}
	return nil, infoOut{Info: info}, nil
}

func (s *Server) termDevices(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, devicesOut, error) {
	if _, err := s.requireRole(req, types.RoleReader); err != nil {
		return nil, devicesOut{}, err
	}
	devices, err := backend.SerialDevices()
	if err != nil {
		return nil, devicesOut{}, err
	}
	return nil, devicesOut{Devices: devices}, nil
}

func decodeInput(data []byte, encoding string) ([]byte, error) {
	switch strings.ToLower(encoding) {
	case "", "utf8", "utf-8", "text":
		return data, nil
	case "base64":
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, fmt.Errorf("invalid_argument: invalid base64 data: %w", err)
		}
		return decoded, nil
	case "hex":
		decoded, err := hex.DecodeString(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, fmt.Errorf("invalid_argument: invalid hex data: %w", err)
		}
		return decoded, nil
	default:
		return nil, fmt.Errorf("invalid_argument: unknown encoding %q", encoding)
	}
}

func formatData(data []byte, format string, encoding string) (string, string) {
	format = strings.ToLower(format)
	encoding = strings.ToLower(encoding)
	if format == "hex" {
		return hex.EncodeToString(data), "hex"
	}
	if encoding == "base64" || (encoding != "hex" && !utf8.Valid(data)) {
		return base64.StdEncoding.EncodeToString(data), "base64"
	}
	if encoding == "hex" {
		return hex.EncodeToString(data), "hex"
	}
	return string(data), "utf8"
}
