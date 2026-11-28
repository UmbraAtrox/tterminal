package buffer

import (
	"context"
	"testing"
	"time"
)

func TestRingReadAndTruncate(t *testing.T) {
	ring := New(8)
	ring.Write([]byte("abcdefghij"))
	base, next := ring.Range()
	if base != 2 || next != 10 {
		t.Fatalf("range = %d..%d, want 2..10", base, next)
	}
	result := ring.Read(0, 4)
	if result.Truncated != true {
		t.Fatal("expected truncated read")
	}
	if string(result.Data) != "cdef" {
		t.Fatalf("data = %q, want %q", result.Data, "cdef")
	}
	if result.Next != 6 {
		t.Fatalf("next = %d, want 6", result.Next)
	}
}

func TestRingReadWindowModes(t *testing.T) {
	ring := New(1024)
	ring.Write([]byte("000\n111\n222\n333\n"))
	result := ring.ReadWindow(0, 0, WindowTail, 1024, 2)
	if string(result.Data) != "222\n333\n" {
		t.Fatalf("tail data = %q", result.Data)
	}
	if result.RangeStart != 0 || result.Cursor != 8 || result.Next != 16 {
		t.Fatalf("tail cursors = %+v", result)
	}
	if result.AvailableBytes != 16 || result.SkippedBytes != 8 || !result.Truncated || result.TruncationReason != "cap" {
		t.Fatalf("tail accounting = %+v", result)
	}

	result = ring.ReadWindow(0, 0, WindowDelta, 4, 1)
	if string(result.Data) != "000\n111\n222\n333\n" {
		t.Fatalf("delta data = %q", result.Data)
	}
	if result.Truncated || result.SkippedBytes != 0 {
		t.Fatalf("delta accounting = %+v", result)
	}

	result = ring.ReadWindow(0, 0, WindowAll, 4, 1)
	if string(result.Data) != "000\n111\n222\n333\n" {
		t.Fatalf("all data = %q", result.Data)
	}
}

func TestRingReadWindowRingDrop(t *testing.T) {
	ring := New(8)
	ring.Write([]byte("abcdefghij"))
	result := ring.ReadWindow(0, 0, WindowAll, 4, 1)
	if string(result.Data) != "cdefghij" {
		t.Fatalf("data = %q", result.Data)
	}
	if result.AvailableBytes != 8 || result.RingDroppedBytes != 2 || !result.Truncated || result.TruncationReason != "ring" {
		t.Fatalf("accounting = %+v", result)
	}
}

func TestRingWait(t *testing.T) {
	ring := New(64)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- ring.Wait(ctx, 0)
	}()
	time.Sleep(10 * time.Millisecond)
	ring.Write([]byte("x"))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
