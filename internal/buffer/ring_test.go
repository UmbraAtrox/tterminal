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
