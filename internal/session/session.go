package session

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/uatx/tterminal/internal/backend"
	"github.com/uatx/tterminal/internal/buffer"
	"github.com/uatx/tterminal/internal/config"
	"github.com/uatx/tterminal/internal/screen"
	"github.com/uatx/tterminal/internal/types"
)

const markerPollMS = 20

type Session struct {
	id      string
	kind    types.BackendKind
	cfg     *config.Config
	backend backend.ByteStreamBackend
	ring    *buffer.Ring
	screen  *screen.Screen

	mu           sync.RWMutex
	createdAt    time.Time
	lastDataAt   time.Time
	cwd          string
	meta         map[string]any
	closed       bool
	oscPending   []byte
	markers      map[string]types.Marker
	markerSignal chan struct{}

	writeMu  sync.Mutex
	doneCh   chan struct{}
	closeOne sync.Once
}

type Info struct {
	ID         string            `json:"id"`
	Backend    types.BackendKind `json:"backend"`
	State      string            `json:"state"`
	PID        int               `json:"pid,omitempty"`
	Cwd        string            `json:"cwd,omitempty"`
	Cols       int               `json:"cols"`
	Rows       int               `json:"rows"`
	CreatedAt  time.Time         `json:"created_at"`
	LastDataAt time.Time         `json:"last_data_at,omitempty"`
	Cursor     int64             `json:"cursor"`
	Exit       types.ExitStatus  `json:"exit"`
	Caps       types.BackendCaps `json:"caps"`
	Meta       map[string]any    `json:"meta,omitempty"`
}

type WaitRequest struct {
	Until     string
	Pattern   string
	QuietMS   int
	MarkerID  string
	Cursor    int64
	TimeoutMS int
	MaxBytes  int
}

type WaitResult struct {
	Reason     string           `json:"reason"`
	NextCursor int64            `json:"next_cursor"`
	Data       []byte           `json:"-"`
	Matches    []string         `json:"matches,omitempty"`
	Exit       types.ExitStatus `json:"exit,omitempty"`
	TimedOut   bool             `json:"timed_out"`
}

type ExecResult struct {
	ID         string           `json:"id"`
	Exit       types.ExitStatus `json:"exit"`
	Cursor     int64            `json:"cursor"`
	NextCursor int64            `json:"next_cursor"`
	TimedOut   bool             `json:"timed_out"`
}

func newSession(id string, kind types.BackendKind, cfg *config.Config, b backend.ByteStreamBackend, cols, rows int, meta map[string]any) *Session {
	if cols <= 0 {
		cols = cfg.Limits.DefaultCols
	}
	if rows <= 0 {
		rows = cfg.Limits.DefaultRows
	}
	if meta == nil {
		meta = map[string]any{}
	}
	return &Session{
		id:           id,
		kind:         kind,
		cfg:          cfg,
		backend:      b,
		ring:         buffer.New(cfg.Limits.MaxRingBytes),
		screen:       screen.New(cols, rows),
		createdAt:    time.Now(),
		meta:         meta,
		markers:      map[string]types.Marker{},
		markerSignal: make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
}

func (s *Session) ID() string              { return s.id }
func (s *Session) Kind() types.BackendKind { return s.kind }
func (s *Session) SetMeta(key string, value any) {
	s.mu.Lock()
	s.meta[key] = value
	s.mu.Unlock()
}
func (s *Session) Meta(key string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.meta[key]
	return value, ok
}
func (s *Session) Ring() *buffer.Ring     { return s.ring }
func (s *Session) Screen() *screen.Screen { return s.screen }
func (s *Session) Done() <-chan struct{}  { return s.doneCh }

func (s *Session) Start(ctx context.Context) error {
	if err := s.backend.Start(ctx); err != nil {
		s.closeOne.Do(func() { close(s.doneCh) })
		return err
	}
	go s.readLoop()
	return nil
}

func (s *Session) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.backend.Read(buf)
		if n > 0 {
			s.ingest(buf[:n])
		}
		if err != nil {
			if shouldIgnoreReadError(err) {
				s.backend.Wait(context.Background())
			}
			break
		}
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.ring.Close()
	s.closeOne.Do(func() { close(s.doneCh) })
}

func shouldIgnoreReadError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
		return true
	}
	if errors.Is(err, syscall.EIO) {
		return true
	}
	return false
}

func (s *Session) ingest(data []byte) {
	s.ring.Write(data)
	s.screen.Write(data)
	s.mu.Lock()
	s.lastDataAt = time.Now()
	s.oscPending = append(s.oscPending, data...)
	if len(s.oscPending) > 64*1024 {
		s.oscPending = s.oscPending[len(s.oscPending)-64*1024:]
	}
	pending := s.oscPending
	s.mu.Unlock()

	remaining, events := parseOSC(pending)
	s.mu.Lock()
	s.oscPending = remaining
	for _, event := range events {
		cursor, _ := s.ring.Range()
		s.markers[event.ID+":"+event.Kind] = types.Marker{
			ID: event.ID, Kind: event.Kind, Code: event.Code, Cursor: cursor, At: time.Now(),
		}
		close(s.markerSignal)
		s.markerSignal = make(chan struct{})
		if event.Kind == "7" && event.Path != "" {
			s.cwd = event.Path
		}
	}
	lastData := s.lastDataAt
	s.mu.Unlock()
	_ = lastData
}

type oscEvent struct {
	Kind string
	ID   string
	Code int
	Path string
}

func parseOSC(data []byte) ([]byte, []oscEvent) {
	var events []oscEvent
	for i := 0; i < len(data); {
		if data[i] != 0x1b || i+1 >= len(data) || data[i+1] != ']' {
			i++
			continue
		}
		start := i + 2
		end := -1
		skip := 0
		for j := start; j < len(data); j++ {
			if data[j] == 0x07 {
				end = j
				skip = 1
				break
			}
			if data[j] == 0x1b && j+1 < len(data) && data[j+1] == '\\' {
				end = j
				skip = 2
				break
			}
		}
		if end < 0 {
			return append([]byte(nil), data[i:]...), events
		}
		payload := string(data[start:end])
		if event, ok := decodeOSC(payload); ok {
			events = append(events, event)
		}
		copy(data[i:], data[end+skip:])
		data = data[:len(data)-(end+skip-i)]
		i = 0
	}
	return append([]byte(nil), data...), events
}

func decodeOSC(payload string) (oscEvent, bool) {
	parts := strings.Split(payload, ";")
	if len(parts) == 0 {
		return oscEvent{}, false
	}
	switch parts[0] {
	case "133":
		if len(parts) < 2 {
			return oscEvent{}, false
		}
		event := oscEvent{Kind: parts[1]}
		for _, param := range parts[2:] {
			if strings.HasPrefix(param, "ttid=") {
				event.ID = strings.TrimPrefix(param, "ttid=")
			}
			if event.Kind == "D" {
				if code, err := parseLeadingInt(param); err == nil {
					event.Code = code
				}
			}
		}
		return event, true
	case "7":
		if len(parts) < 2 {
			return oscEvent{}, false
		}
		event := oscEvent{Kind: "7"}
		raw := strings.Join(parts[1:], ";")
		if parsed, err := url.Parse(raw); err == nil {
			event.Path = parsed.Path
		} else {
			event.Path = raw
		}
		return event, true
	default:
		return oscEvent{}, false
	}
}

func parseLeadingInt(value string) (int, error) {
	end := 0
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, fmt.Errorf("invalid integer")
	}
	result := 0
	for _, c := range value[:end] {
		result = result*10 + int(c-'0')
	}
	return result, nil
}

func (s *Session) Write(data []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	select {
	case <-s.doneCh:
		return 0, fmt.Errorf("session_closed: session %s is closed", s.id)
	default:
	}
	return s.backend.Write(data)
}

func (s *Session) WriteText(value string, encoding string, enter bool) error {
	data, err := decodeData([]byte(value), encoding)
	if err != nil {
		return err
	}
	if enter {
		data = append(data, '\r')
	}
	_, err = s.Write(data)
	return err
}

func decodeData(data []byte, encoding string) ([]byte, error) {
	switch strings.ToLower(encoding) {
	case "", "utf8", "utf-8", "text":
		return data, nil
	case "base64":
		return decodeBase64(string(data))
	case "hex":
		return decodeHex(string(data))
	default:
		return nil, fmt.Errorf("invalid_argument: unknown encoding %q", encoding)
	}
}

func (s *Session) Exec(ctx context.Context, command string, timeoutMS int) (ExecResult, error) {
	if strings.ContainsAny(command, "\r\n") {
		return ExecResult{}, fmt.Errorf("invalid_argument: command must be a single line")
	}
	id := randomID(8)
	_, start := s.ring.Range()
	line := fmt.Sprintf("printf '\\033]133;C;ttid=%s\\007'; %s; __tt_rc=$?; printf '\\033]133;D;%%d;ttid=%s\\007' \"$__tt_rc\"\r", id, command, id)
	if _, err := s.Write([]byte(line)); err != nil {
		return ExecResult{}, err
	}
	marker, ok := s.waitForMarker(ctx, id, "D", timeoutMS)
	result := ExecResult{ID: id, Cursor: start, TimedOut: !ok}
	if ok {
		result.Exit = types.ExitStatus{Exited: true, Code: marker.Code}
		result.NextCursor = marker.Cursor
	} else {
		_, result.NextCursor = s.ring.Range()
	}
	return result, nil
}

func (s *Session) waitForMarker(ctx context.Context, id string, kind string, timeoutMS int) (types.Marker, bool) {
	if timeoutMS <= 0 {
		timeoutMS = s.cfg.Limits.WaitTimeoutMS
	}
	deadline := time.NewTimer(time.Duration(timeoutMS) * time.Millisecond)
	defer deadline.Stop()
	ticker := time.NewTicker(markerPollMS * time.Millisecond)
	defer ticker.Stop()
	for {
		if marker, ok := s.marker(id, kind); ok {
			return marker, true
		}
		select {
		case <-s.markerSignal:
		case <-ticker.C:
		case <-deadline.C:
			return types.Marker{}, false
		case <-ctx.Done():
			return types.Marker{}, false
		}
	}
}

func (s *Session) marker(id string, kind string) (types.Marker, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	marker, ok := s.markers[id+":"+kind]
	return marker, ok
}

func (s *Session) Wait(ctx context.Context, req WaitRequest) (WaitResult, error) {
	if req.TimeoutMS <= 0 {
		req.TimeoutMS = s.cfg.Limits.WaitTimeoutMS
	}
	if req.MaxBytes <= 0 {
		req.MaxBytes = s.cfg.Limits.MaxChunkBytes
	}
	deadline := time.NewTimer(time.Duration(req.TimeoutMS) * time.Millisecond)
	defer deadline.Stop()
	interval := s.cfg.Limits.QuietPollMS
	if interval <= 0 {
		interval = 25
	}
	ticker := time.NewTicker(time.Duration(interval) * time.Millisecond)
	defer ticker.Stop()

	var regex *regexp.Regexp
	if req.Until == "regex" {
		compiled, err := regexp.Compile(req.Pattern)
		if err != nil {
			return WaitResult{}, fmt.Errorf("invalid_argument: bad regex: %w", err)
		}
		regex = compiled
	}

	for {
		result, done, err := s.checkWait(req, regex)
		if err != nil {
			return WaitResult{}, err
		}
		if done {
			return result, nil
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			result, _, _ = s.checkWait(req, regex)
			result.TimedOut = true
			result.Reason = "timeout"
			return result, nil
		case <-s.doneCh:
			result, done, _ := s.checkWait(req, regex)
			if done || req.Until == "exit" || req.Until == "closed" {
				return result, nil
			}
		case <-ctx.Done():
			return WaitResult{}, ctx.Err()
		}
	}
}

func (s *Session) checkWait(req WaitRequest, regex *regexp.Regexp) (WaitResult, bool, error) {
	base, next := s.ring.Range()
	start := req.Cursor
	if start < base {
		start = base
	}
	result := WaitResult{NextCursor: next}
	if start <= next {
		result.Data = s.ring.Read(start, req.MaxBytes).Data
	}

	switch req.Until {
	case "", "data":
		if next > req.Cursor {
			result.Reason = "data"
			return result, true, nil
		}
	case "exit", "closed":
		status, err := s.backend.Status(context.Background())
		if err != nil {
			return result, false, err
		}
		if status.State == "exited" || status.State == "closed" {
			result.Reason = status.State
			result.Exit = status.Exit
			return result, true, nil
		}
	case "regex":
		if regex != nil && len(result.Data) > 0 {
			matches := regex.FindAllString(string(result.Data), 8)
			if len(matches) > 0 {
				result.Reason = "regex"
				result.Matches = matches
				return result, true, nil
			}
		}
	case "quiet":
		s.mu.RLock()
		last := s.lastDataAt
		s.mu.RUnlock()
		if !last.IsZero() && time.Since(last) >= time.Duration(req.QuietMS)*time.Millisecond {
			result.Reason = "quiet"
			return result, true, nil
		}
	case "marker":
		if marker, ok := s.marker(req.MarkerID, "D"); ok {
			result.Reason = "marker"
			result.Exit = types.ExitStatus{Exited: true, Code: marker.Code}
			result.NextCursor = marker.Cursor
			return result, true, nil
		}
	default:
		return result, false, fmt.Errorf("invalid_argument: unknown wait condition %q", req.Until)
	}
	result.NextCursor = next
	return result, false, nil
}

func (s *Session) Status(ctx context.Context) (types.BackendStatus, error) {
	return s.backend.Status(ctx)
}

func (s *Session) Info(ctx context.Context) (Info, error) {
	status, err := s.backend.Status(ctx)
	if err != nil {
		return Info{}, err
	}
	_, cursor := s.ring.Range()
	s.mu.RLock()
	cwd := s.cwd
	last := s.lastDataAt
	meta := cloneMeta(s.meta)
	s.mu.RUnlock()
	if cwd == "" {
		if value, ok := meta["cwd"].(string); ok {
			cwd = value
		}
	}
	cols, rows := s.screenSize()
	return Info{
		ID: s.id, Backend: s.kind, State: status.State, PID: status.PID, Cwd: cwd,
		Cols: cols, Rows: rows, CreatedAt: s.createdAt, LastDataAt: last, Cursor: cursor,
		Exit: status.Exit, Caps: status.Caps, Meta: meta,
	}, nil
}

func (s *Session) screenSize() (int, int) {
	snapshot := s.screen.Snapshot()
	return snapshot.Cols, snapshot.Rows
}

func (s *Session) Resize(cols, rows int) error {
	if err := s.backend.Resize(cols, rows); err != nil {
		return err
	}
	s.screen.Resize(cols, rows)
	return nil
}

func (s *Session) Signal(name string) error {
	return s.backend.Signal(name)
}

func (s *Session) Control(ctx context.Context, action string, value map[string]any) (map[string]any, error) {
	switch action {
	case "clear_ring":
		s.ring.Clear()
		return map[string]any{"ok": true}, nil
	case "clear_screen":
		s.screen.Write([]byte("\x1b[2J\x1b[H"))
		return map[string]any{"ok": true}, nil
	case "set_ring_limit":
		limit, ok := value["max_ring_bytes"].(float64)
		if !ok {
			return nil, fmt.Errorf("invalid_argument: max_ring_bytes must be a number")
		}
		s.ring.SetLimit(int(limit))
		return map[string]any{"ok": true}, nil
	}
	controllable, ok := s.backend.(backend.Controllable)
	if !ok {
		return nil, backend.Unsupported("control/"+action, s.kind)
	}
	return controllable.Control(ctx, backend.ControlRequest{Action: action, Value: value})
}

func (s *Session) Close() error {
	var err error
	s.closeOne.Do(func() {
		err = s.backend.Close()
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.ring.Close()
		select {
		case <-s.doneCh:
		default:
			close(s.doneCh)
		}
	})
	return err
}

func cloneMeta(meta map[string]any) map[string]any {
	if len(meta) == 0 {
		return map[string]any{}
	}
	cloned, err := json.Marshal(meta)
	if err != nil {
		return map[string]any{}
	}
	out := map[string]any{}
	_ = json.Unmarshal(cloned, &out)
	return out
}

func randomID(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func decodeBase64(value string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("invalid_argument: invalid base64 data: %w", err)
	}
	return data, nil
}

func decodeHex(value string) ([]byte, error) {
	data, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("invalid_argument: invalid hex data: %w", err)
	}
	return data, nil
}
