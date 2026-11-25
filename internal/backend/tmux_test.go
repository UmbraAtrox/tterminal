package backend

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestTmuxBackendLifecycle(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	socket := fmt.Sprintf("tterminal-test-%d-%d", os.Getuid(), time.Now().UnixNano())
	defer func() {
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	}()

	b := NewTmux(TmuxOptions{
		SocketName:   socket,
		Session:      "tterminal-test",
		Create:       true,
		Command:      "printf 'TMUX_OK\\n'; sleep 30",
		Cols:         80,
		Rows:         24,
		RemainOnExit: true,
	})
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	outputCh := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(b)
		outputCh <- string(data)
	}()

	deadline := time.Now().Add(5 * time.Second)
	output := ""
	for time.Now().Before(deadline) {
		select {
		case output = <-outputCh:
			deadline = time.Now().Add(-time.Second)
		case <-time.After(50 * time.Millisecond):
			captured, err := b.Control(context.Background(), ControlRequest{Action: "capture_pane"})
			if err == nil {
				if text, ok := captured["text"].(string); ok && strings.Contains(text, "TMUX_OK") {
					output = text
					break
				}
			}
		}
	}
	if !strings.Contains(output, "TMUX_OK") {
		t.Fatalf("output = %q, want TMUX_OK", output)
	}
	if err := b.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	if err := b.Signal("INT"); err != nil {
		t.Fatal(err)
	}
	status, err := b.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Exited {
		t.Fatalf("status = %+v, want exited", status)
	}
}
