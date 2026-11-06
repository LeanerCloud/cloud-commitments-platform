package ctxutil

import (
	"context"
	"testing"
	"time"
)

type ctxKey struct{}

func TestDetachedWithTimeout(t *testing.T) {
	parent, cancelParent := context.WithTimeout(context.WithValue(context.Background(), ctxKey{}, "req-1"), time.Millisecond)
	cancelParent()

	start := time.Now()
	ctx, cancel := DetachedWithTimeout(parent, time.Minute)
	defer cancel()

	if err := ctx.Err(); err != nil {
		t.Fatalf("parent cancellation must not reach the detached context, got %v", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("detached context must carry its own deadline")
	}
	if d := deadline.Sub(start); d < 59*time.Second || d > 61*time.Second {
		t.Fatalf("deadline must be ~1m from now (not the parent's), got %s", d)
	}
	if got := ctx.Value(ctxKey{}); got != "req-1" {
		t.Fatalf("detached context must keep parent values, got %v", got)
	}

	cancel()
	if ctx.Err() == nil {
		t.Fatal("the returned CancelFunc must cancel the detached context")
	}
}

func TestDetachedWithDeadline(t *testing.T) {
	parent, cancelParent := context.WithTimeout(context.WithValue(context.Background(), ctxKey{}, "req-2"), time.Millisecond)
	cancelParent()

	want := time.Now().Add(time.Hour)
	ctx, cancel := DetachedWithDeadline(parent, want)
	defer cancel()

	if err := ctx.Err(); err != nil {
		t.Fatalf("parent cancellation must not reach the detached context, got %v", err)
	}
	got, ok := ctx.Deadline()
	if !ok || !got.Equal(want) {
		t.Fatalf("deadline must be exactly the absolute one given: got %v ok=%v want %v", got, ok, want)
	}
	if v := ctx.Value(ctxKey{}); v != "req-2" {
		t.Fatalf("detached context must keep parent values, got %v", v)
	}

	past, cancelPast := DetachedWithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelPast()
	if past.Err() == nil {
		t.Fatal("a deadline in the past must give an already-done context")
	}
}
