package server

import (
	"context"
	"errors"
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
	entered chan struct{}
	release chan struct{}
}

func (r *blockingResolver) GetSecret(context.Context, string) (string, error) {
	close(r.entered)
	<-r.release
	return "", errors.New("released")
}

// TestStartHTTPServerWaitsForBackgroundInitOnShutdown pins #664: StartHTTPServer
// must not return (letting main run app.Close) while the background initializer
// is still inside ensureDB, because both touch app.DB.
func TestStartHTTPServerWaitsForBackgroundInitOnShutdown(t *testing.T) {
	resolver := &blockingResolver{entered: make(chan struct{}), release: make(chan struct{})}
	app := &Application{
		Version:        "test",
		dbConfig:       &database.Config{PasswordSecret: "db-password"},
		secretResolver: resolver,
		initRetryDelay: time.Millisecond,
	}

	returned := make(chan error, 1)
	go func() { returned <- StartHTTPServer(app, 0) }()

	select {
	case <-resolver.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("background initialization never started")
	}

	// The signal handler is installed before the initializer starts, so the
	// process-wide SIGTERM is caught by StartHTTPServer's NotifyContext.
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}

	select {
	case <-returned:
		t.Fatal("StartHTTPServer returned while background initialization was still running")
	case <-time.After(300 * time.Millisecond):
	}

	close(resolver.release)
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("StartHTTPServer returned %v after initialization stopped", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StartHTTPServer did not return after the initializer exited")
	}
}
