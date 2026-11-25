package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/uatx/tterminal/internal/config"
	"github.com/uatx/tterminal/internal/types"
)

func TestSessionWaitAndScreen(t *testing.T) {
	cfg := config.Default()
	manager := NewManager(cfg)
	defer manager.Shutdown()

	created, err := manager.Open(context.Background(), OpenRequest{
		Backend: types.BackendPTY,
		PTY:     PTYOpen{Argv: []string{"/bin/sh", "-c", "printf hello; sleep 0.1; printf world"}},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := created.Wait(ctx, WaitRequest{Until: "regex", Pattern: "world", TimeoutMS: 2000, MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != "regex" {
		t.Fatalf("reason = %q, want regex", result.Reason)
	}
	data := created.Ring().Read(0, 4096).Data
	if !strings.Contains(string(data), "hello") || !strings.Contains(string(data), "world") {
		t.Fatalf("data = %q", data)
	}
	snapshot := created.Screen().Snapshot()
	if !strings.Contains(snapshot.Text, "hello") {
		t.Fatalf("screen = %q", snapshot.Text)
	}
}

func TestSessionExec(t *testing.T) {
	cfg := config.Default()
	manager := NewManager(cfg)
	defer manager.Shutdown()

	created, err := manager.Open(context.Background(), OpenRequest{
		Backend: types.BackendPTY,
		PTY:     PTYOpen{Argv: []string{"/bin/sh", "-i"}},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	result, err := created.Exec(context.Background(), "printf EXEC_OK", 3000)
	if err != nil {
		t.Fatal(err)
	}
	if result.TimedOut || result.Exit.Code != 0 {
		t.Fatalf("exec result = %+v", result)
	}
	data := created.Ring().Read(result.Cursor, 4096).Data
	if !strings.Contains(string(data), "EXEC_OK") {
		t.Fatalf("data = %q", string(data))
	}
}
