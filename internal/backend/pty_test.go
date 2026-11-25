package backend

import (
	"context"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPTYBackendExitStatus(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	b := NewPTY(PTYOptions{Argv: []string{"/bin/sh", "-c", "printf hello; exit 7"}, Cols: 80, Rows: 24})
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	outputCh := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(b)
		outputCh <- string(data)
	}()
	status, err := b.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Exited || status.Code != 7 {
		t.Fatalf("status = %+v, want exit code 7", status)
	}
	select {
	case out := <-outputCh:
		if !strings.Contains(out, "hello") {
			t.Fatalf("output = %q, want hello", out)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out reading pty output")
	}
}

func TestPTYBackendSignal(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	b := NewPTY(PTYOptions{Argv: []string{"/bin/sh", "-c", "sleep 30"}, Cols: 80, Rows: 24})
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	go io.Copy(io.Discard, b)
	time.Sleep(150 * time.Millisecond)
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
