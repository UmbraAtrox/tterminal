package backend

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/uatx/tterminal/internal/types"
	"go.bug.st/serial"
)

type SerialOptions struct {
	Device       string
	BaudRate     int
	DataBits     int
	Parity       string
	StopBits     string
	FlowControl  string
	DTR          *bool
	RTS          *bool
	ReadTimeout  time.Duration
	BreakSeconds time.Duration
}

type SerialBackend struct {
	opts SerialOptions
	port serial.Port

	mu       sync.Mutex
	status   types.BackendStatus
	doneCh   chan struct{}
	closeOne sync.Once
}

func NewSerial(opts SerialOptions) *SerialBackend {
	if opts.BaudRate <= 0 {
		opts.BaudRate = 115200
	}
	if opts.DataBits == 0 {
		opts.DataBits = 8
	}
	if opts.Parity == "" {
		opts.Parity = "none"
	}
	if opts.StopBits == "" {
		opts.StopBits = "1"
	}
	if opts.FlowControl == "" {
		opts.FlowControl = "none"
	}
	return &SerialBackend{
		opts:   opts,
		doneCh: make(chan struct{}),
		status: types.BackendStatus{
			State:     "initializing",
			Caps:      serialCaps(),
			StartedAt: time.Now(),
		},
	}
}

func serialCaps() types.BackendCaps {
	return types.BackendCaps{CanResize: true, CanBreak: true, CanSetModem: true}
}

func (b *SerialBackend) Kind() types.BackendKind         { return types.BackendSerial }
func (b *SerialBackend) Capabilities() types.BackendCaps { return serialCaps() }

func (b *SerialBackend) Start(ctx context.Context) error {
	if b.opts.Device == "" {
		return fmt.Errorf("invalid_argument: device is required for serial backend")
	}
	parity, err := parseParity(b.opts.Parity)
	if err != nil {
		return err
	}
	stopBits, err := parseStopBits(b.opts.StopBits)
	if err != nil {
		return err
	}
	initial := &serial.ModemOutputBits{}
	initial.DTR = true
	initial.RTS = true
	if b.opts.FlowControl != "none" {
		initial.RTS = false
	}
	if b.opts.DTR != nil {
		initial.DTR = *b.opts.DTR
	}
	if b.opts.RTS != nil {
		initial.RTS = *b.opts.RTS
	}
	mode := &serial.Mode{
		BaudRate:          b.opts.BaudRate,
		DataBits:          b.opts.DataBits,
		Parity:            parity,
		StopBits:          stopBits,
		InitialStatusBits: initial,
	}
	port, err := serial.Open(b.opts.Device, mode)
	if err != nil {
		return fmt.Errorf("serial_open: %w", err)
	}
	timeout := b.opts.ReadTimeout
	if timeout == 0 {
		timeout = serial.NoTimeout
	}
	if err := port.SetReadTimeout(timeout); err != nil {
		port.Close()
		return fmt.Errorf("serial_timeout: %w", err)
	}

	b.mu.Lock()
	b.port = port
	b.status.State = "running"
	b.status.Detail = b.opts.Device
	b.mu.Unlock()
	return nil
}

func (b *SerialBackend) Read(p []byte) (int, error) {
	b.mu.Lock()
	port := b.port
	b.mu.Unlock()
	if port == nil {
		return 0, fmt.Errorf("serial_closed: port is not open")
	}
	return port.Read(p)
}

func (b *SerialBackend) Write(p []byte) (int, error) {
	b.mu.Lock()
	port := b.port
	b.mu.Unlock()
	if port == nil {
		return 0, fmt.Errorf("serial_closed: port is not open")
	}
	return port.Write(p)
}

func (b *SerialBackend) Wait(ctx context.Context) (types.ExitStatus, error) {
	select {
	case <-b.doneCh:
		return types.ExitStatus{}, nil
	case <-ctx.Done():
		return types.ExitStatus{}, ctx.Err()
	}
}

func (b *SerialBackend) Status(ctx context.Context) (types.BackendStatus, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	status := b.status
	status.Caps = serialCaps()
	return status, nil
}

func (b *SerialBackend) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("invalid_argument: cols and rows must be positive")
	}
	return nil
}

func (b *SerialBackend) Signal(name string) error {
	if strings.EqualFold(strings.TrimPrefix(name, "SIG"), "BRK") {
		return b.Break()
	}
	return Unsupported("signal", types.BackendSerial)
}

func (b *SerialBackend) Break() error {
	b.mu.Lock()
	port := b.port
	b.mu.Unlock()
	if port == nil {
		return fmt.Errorf("serial_closed: port is not open")
	}
	seconds := b.opts.BreakSeconds
	if seconds <= 0 {
		seconds = 250 * time.Millisecond
	}
	if err := port.Break(seconds); err != nil {
		return fmt.Errorf("serial_break: %w", err)
	}
	return nil
}

func (b *SerialBackend) Control(ctx context.Context, req ControlRequest) (map[string]any, error) {
	b.mu.Lock()
	port := b.port
	b.mu.Unlock()
	if port == nil {
		return nil, fmt.Errorf("serial_closed: port is not open")
	}
	switch req.Action {
	case "set_dtr":
		value, ok := req.Value["value"].(bool)
		if !ok {
			return nil, fmt.Errorf("invalid_argument: value must be a boolean")
		}
		if err := port.SetDTR(value); err != nil {
			return nil, fmt.Errorf("serial_control: %w", err)
		}
	case "set_rts":
		value, ok := req.Value["value"].(bool)
		if !ok {
			return nil, fmt.Errorf("invalid_argument: value must be a boolean")
		}
		if err := port.SetRTS(value); err != nil {
			return nil, fmt.Errorf("serial_control: %w", err)
		}
	case "flush_input":
		if err := port.ResetInputBuffer(); err != nil {
			return nil, fmt.Errorf("serial_control: %w", err)
		}
	case "flush_output":
		if err := port.ResetOutputBuffer(); err != nil {
			return nil, fmt.Errorf("serial_control: %w", err)
		}
	case "drain":
		if err := port.Drain(); err != nil {
			return nil, fmt.Errorf("serial_control: %w", err)
		}
	case "modem_status":
		bits, err := port.GetModemStatusBits()
		if err != nil {
			return nil, fmt.Errorf("serial_control: %w", err)
		}
		return map[string]any{
			"cts": bits.CTS,
			"dsr": bits.DSR,
			"ri":  bits.RI,
			"dcd": bits.DCD,
		}, nil
	case "set_mode":
		baud, _ := req.Value["baud_rate"].(int)
		if baud <= 0 {
			baud = b.opts.BaudRate
		}
		if err := port.SetMode(&serial.Mode{BaudRate: baud, DataBits: b.opts.DataBits, Parity: mustParity(b.opts.Parity), StopBits: mustStopBits(b.opts.StopBits)}); err != nil {
			return nil, fmt.Errorf("serial_control: %w", err)
		}
		b.opts.BaudRate = baud
	default:
		return nil, fmt.Errorf("invalid_argument: unknown serial control action %q", req.Action)
	}
	return map[string]any{"ok": true}, nil
}

func (b *SerialBackend) Close() error {
	var err error
	b.closeOne.Do(func() {
		b.mu.Lock()
		port := b.port
		b.status.State = "closed"
		b.status.EndedAt = time.Now()
		b.mu.Unlock()
		if port != nil {
			err = port.Close()
		}
		close(b.doneCh)
	})
	return err
}

func parseParity(value string) (serial.Parity, error) {
	switch strings.ToLower(value) {
	case "none", "n":
		return serial.NoParity, nil
	case "even", "e":
		return serial.EvenParity, nil
	case "odd", "o":
		return serial.OddParity, nil
	case "mark", "m":
		return serial.MarkParity, nil
	case "space", "s":
		return serial.SpaceParity, nil
	default:
		return 0, fmt.Errorf("invalid_argument: unknown parity %q", value)
	}
}

func mustParity(value string) serial.Parity {
	parity, _ := parseParity(value)
	return parity
}

func parseStopBits(value string) (serial.StopBits, error) {
	switch value {
	case "1":
		return serial.OneStopBit, nil
	case "1.5":
		return serial.OnePointFiveStopBits, nil
	case "2":
		return serial.TwoStopBits, nil
	default:
		return 0, fmt.Errorf("invalid_argument: unknown stop_bits %q", value)
	}
}

func mustStopBits(value string) serial.StopBits {
	stopBits, _ := parseStopBits(value)
	return stopBits
}

func SerialDevices() ([]string, error) {
	devices, err := serial.GetPortsList()
	if err != nil {
		return nil, fmt.Errorf("serial_devices: %w", err)
	}
	if devices == nil {
		devices = []string{}
	}
	return devices, nil
}
