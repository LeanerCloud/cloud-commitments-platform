package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/secrets"
)

// blockingResolver holds ensureDB inside secret resolution, the one step with no
// timeout of its own, so a background initialization attempt is provably still
// running when the shutdown signal arrives.
type blockingResolver struct {
	secrets.Resolver
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
	releaseOnce sync.Once
}

func (r *blockingResolver) GetSecret(context.Context, string) (string, error) {
	r.enteredOnce.Do(func() { close(r.entered) })
	<-r.release
	return "", errors.New("released")
}

func (r *blockingResolver) unblock() { r.releaseOnce.Do(func() { close(r.release) }) }

// startServerWithBlockedInit runs StartHTTPServer on a free port with the
// background initializer parked inside GetSecret, and returns once it is parked.
// The resolver is released on cleanup so a failing test cannot leak the goroutine.
func startServerWithBlockedInit(t *testing.T) (port int, resolver *blockingResolver, returned <-chan error) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port = l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}

	resolver = &blockingResolver{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(resolver.unblock)
	app := &Application{
		Version:        "test",
		dbConfig:       &database.Config{PasswordSecret: "db-password"},
		secretResolver: resolver,
		initRetryDelay: time.Millisecond,
	}

	ret := make(chan error, 1)
	go func() { ret <- StartHTTPServer(app, port) }()

	select {
	case <-resolver.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("background initialization never started")
	}

	// Wait until the listener is bound. Otherwise a later "port is closed" check
	// could pass simply because the server had not started listening yet.
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never started listening on %s: %v", addr, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return port, resolver, ret
}

// sendSIGTERM signals this process. It is only safe after StartHTTPServer has
// installed its NotifyContext, which happens before the initializer starts.
func sendSIGTERM(t *testing.T) {
	t.Helper()
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
}

// TestStartHTTPServerWaitsForBackgroundInitOnShutdown pins #664: StartHTTPServer
// must not return (letting main run app.Close) while the background initializer
// is still inside ensureDB, because both touch app.DB. It also pins the order:
// the listener is closed (Shutdown ran) while the initializer is still parked,
// so the wait comes after draining, not before.
func TestStartHTTPServerWaitsForBackgroundInitOnShutdown(t *testing.T) {
	port, resolver, returned := startServerWithBlockedInit(t)
	sendSIGTERM(t)

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			break
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("listener still accepting connections while shutdown should have started")
		}
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case <-returned:
		t.Fatal("StartHTTPServer returned while background initialization was still running")
	case <-time.After(300 * time.Millisecond):
	}

	resolver.unblock()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("StartHTTPServer returned %v after initialization stopped", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StartHTTPServer did not return after the initializer exited")
	}
}

// TestStartHTTPServerShutdownWaitIsBounded pins the grace-period bound: an
// initializer that never exits (a wedged secret store ignoring its context)
// must delay shutdown by at most shutdownGracePeriod, not forever.
func TestStartHTTPServerShutdownWaitIsBounded(t *testing.T) {
	old := shutdownGracePeriod
	shutdownGracePeriod = 200 * time.Millisecond
	t.Cleanup(func() { shutdownGracePeriod = old })

	_, _, returned := startServerWithBlockedInit(t)
	sendSIGTERM(t)

	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("StartHTTPServer returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StartHTTPServer did not return after the shutdown grace period with the initializer still blocked")
	}
}
