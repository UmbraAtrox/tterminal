package screen

import (
	"sync"

	vt "github.com/unixshells/vt-go"
)

type Snapshot struct {
	Text      string `json:"text"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
	CursorX   int    `json:"cursor_x"`
	CursorY   int    `json:"cursor_y"`
	AltScreen bool   `json:"alt_screen"`
}

type Screen struct {
	mu   sync.Mutex
	emu  *vt.SafeEmulator
	cols int
	rows int
}

func New(cols, rows int) *Screen {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	return &Screen{emu: vt.NewSafeEmulator(cols, rows), cols: cols, rows: rows}
}

func (s *Screen) Write(p []byte) {
	if len(p) == 0 {
		return
	}
	s.mu.Lock()
	_, _ = s.emu.Write(p)
	s.mu.Unlock()
}

func (s *Screen) Resize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	s.mu.Lock()
	s.emu.Resize(cols, rows)
	s.cols = cols
	s.rows = rows
	s.mu.Unlock()
}

func (s *Screen) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	pos := s.emu.CursorPosition()
	return Snapshot{
		Text:      s.emu.Render(),
		Cols:      s.cols,
		Rows:      s.rows,
		CursorX:   pos.X,
		CursorY:   pos.Y,
		AltScreen: s.emu.IsAltScreen(),
	}
}
