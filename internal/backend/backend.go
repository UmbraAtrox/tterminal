package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall"

	"github.com/uatx/tterminal/internal/types"
)

type ByteStreamBackend interface {
	io.ReadWriteCloser
	Start(ctx context.Context) error
	Wait(ctx context.Context) (types.ExitStatus, error)
	Close() error
	Kind() types.BackendKind
	Capabilities() types.BackendCaps
	Status(ctx context.Context) (types.BackendStatus, error)
	Resize(cols, rows int) error
	Signal(signal string) error
}

type ControlRequest struct {
	Action string         `json:"action"`
	Value  map[string]any `json:"value,omitempty"`
}

type Controllable interface {
	Control(ctx context.Context, req ControlRequest) (map[string]any, error)
}

type UnsupportedError struct {
	Op      string
	Backend types.BackendKind
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("unsupported_for_backend: %s is not available for %s sessions", e.Op, e.Backend)
}

func Unsupported(op string, kind types.BackendKind) error {
	return &UnsupportedError{Op: op, Backend: kind}
}

func IsUnsupported(err error) bool {
	var target *UnsupportedError
	return errors.As(err, &target)
}

var signalNames = map[string]syscall.Signal{
	"HUP":   syscall.SIGHUP,
	"INT":   syscall.SIGINT,
	"QUIT":  syscall.SIGQUIT,
	"ILL":   syscall.SIGILL,
	"TRAP":  syscall.SIGTRAP,
	"ABRT":  syscall.SIGABRT,
	"BUS":   syscall.SIGBUS,
	"FPE":   syscall.SIGFPE,
	"KILL":  syscall.SIGKILL,
	"USR1":  syscall.SIGUSR1,
	"SEGV":  syscall.SIGSEGV,
	"USR2":  syscall.SIGUSR2,
	"PIPE":  syscall.SIGPIPE,
	"ALRM":  syscall.SIGALRM,
	"TERM":  syscall.SIGTERM,
	"TSTP":  syscall.SIGTSTP,
	"CONT":  syscall.SIGCONT,
	"WINCH": syscall.SIGWINCH,
}

func ParseSignal(name string) (syscall.Signal, error) {
	upper := strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(name, "SIG")))
	sig, ok := signalNames[upper]
	if !ok {
		return 0, fmt.Errorf("invalid_argument: unknown signal %q", name)
	}
	return sig, nil
}

func SignalName(sig syscall.Signal) string {
	for name, value := range signalNames {
		if value == sig {
			return name
		}
	}
	return sig.String()
}
