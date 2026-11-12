package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/api"
)

type httpInitialization struct {
	done chan struct{}
	err  error
}

// StartHTTPServer initializes this instance and serves until SIGINT or SIGTERM.
func StartHTTPServer(app *Application, port int) error {
	ctx, cancel := context.WithCancel(context.Background())
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	signalsStopped := make(chan struct{})
	go func() {
		select {
		case <-signals:
			signal.Stop(signals)
			cancel()
		case <-ctx.Done():
			signal.Stop(signals)
		}
		close(signalsStopped)
	}()
	defer func() {
		cancel()
		<-signalsStopped
	}()
	return startHTTPServer(ctx, app, port)
}

func startHTTPServer(ctx context.Context, app *Application, port int) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	initialization := &httpInitialization{done: make(chan struct{})}
	app.httpInit = initialization
	server := CreateHTTPServer(app, port)
	log.Printf("Starting HTTP server on %s", server.Addr)

	go func() {
		initialization.err = app.ensureDB(ctx)
		close(initialization.done)
	}()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()

	initDone := initialization.done
	var result error
	var serverExited bool
wait:
	for {
		select {
		case <-initDone:
			if initialization.err != nil && !errors.Is(initialization.err, ctx.Err()) {
				result = fmt.Errorf("initialize HTTP server: %w", initialization.err)
				break wait
			}
			initDone = nil
		case err := <-serveErr:
			serverExited = true
			if !errors.Is(err, http.ErrServerClosed) {
				result = err
			}
			break wait
		case <-ctx.Done():
			break wait
		}
	}

	cancel()
	shutdownCtx, stopShutdown := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutdown: %v", err)
	}
	// The migration driver can ignore cancellation; wait before the caller closes the pool.
	<-initialization.done
	if !serverExited {
		<-serveErr
	}
	return result
}

func (app *Application) ensureHTTPDB(ctx context.Context) error {
	if app.httpInit == nil {
		return app.ensureDB(ctx)
	}
	select {
	case <-app.httpInit.done:
		return app.httpInit.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (app *Application) currentAPI() *api.Handler {
	app.apiMu.RLock()
	defer app.apiMu.RUnlock()
	return app.API
}
