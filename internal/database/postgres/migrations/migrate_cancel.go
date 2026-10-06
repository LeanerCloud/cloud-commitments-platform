package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// cancelRequestTimeout bounds the out-of-band PostgreSQL cancel requests sent
// when a migration context ends.
const cancelRequestTimeout = 5 * time.Second

type cancelRequestKey struct{}

// connTracker lets a context stop golang-migrate's pgx driver, which ignores
// contexts: it pings, takes pg_advisory_lock and runs migration SQL with
// context.Background. The tracker records every connection the migrator opens
// and, when the context ends, sends a PostgreSQL cancel request for each (this
// interrupts lock waits and running SQL server-side, rolling back the open
// transaction) and then closes the sockets so any blocked read returns.
type connTracker struct {
	ctx context.Context

	mu       sync.Mutex
	aborted  bool
	sessions []*pgconn.PgConn
	sockets  []net.Conn
}

func (t *connTracker) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	// Cancel requests dial through this same function with their own context;
	// everything else is bound to the migration context so a stalled connect
	// returns when it ends.
	isCancel := ctx.Value(cancelRequestKey{}) != nil
	if !isCancel {
		var stop context.CancelFunc
		ctx, stop = context.WithCancel(ctx)
		defer stop()
		defer context.AfterFunc(t.ctx, stop)()
	}

	conn, err := (&net.Dialer{KeepAlive: 5 * time.Minute}).DialContext(ctx, network, addr)
	if err != nil || isCancel {
		return conn, err
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.aborted {
		_ = conn.Close()
		return nil, t.ctx.Err()
	}
	t.sockets = append(t.sockets, conn)
	return conn, nil
}

func (t *connTracker) afterConnect(_ context.Context, conn *pgx.Conn) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.aborted {
		return t.ctx.Err()
	}
	t.sessions = append(t.sessions, conn.PgConn())
	return nil
}

func (t *connTracker) abort() {
	t.mu.Lock()
	t.aborted = true
	sessions := append([]*pgconn.PgConn(nil), t.sessions...)
	sockets := append([]net.Conn(nil), t.sockets...)
	t.mu.Unlock()

	cctx, stop := context.WithTimeout(context.WithValue(context.Background(), cancelRequestKey{}, true), cancelRequestTimeout)
	defer stop()
	for _, s := range sessions {
		if err := s.CancelRequest(cctx); err != nil {
			log.Printf("migrate: cancel request failed: %v", err)
		}
	}
	for _, c := range sockets {
		_ = c.Close()
	}
}

// watch aborts the tracked connections once ctx ends. The returned function
// stops the watcher and waits for an abort already in flight.
func (t *connTracker) watch() func() {
	finished := make(chan struct{})
	stop := context.AfterFunc(t.ctx, func() {
		defer close(finished)
		t.abort()
	})
	return func() {
		if !stop() {
			<-finished
		}
	}
}

// newCancelableMigrator is migrate.New for the pgx5 driver with the migrator's
// connections wired to ctx. It reproduces what the driver's Open does with the
// DSN (database name, default options) so advisory-lock IDs stay identical to
// other replicas running the stock constructor.
func newCancelableMigrator(tracker *connTracker, migrationsPath, dsn string) (*migrate.Migrate, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to parse migration DSN: %w", err)
	}
	u.Scheme = "postgres"
	connCfg, err := pgx.ParseConfig(u.String())
	if err != nil {
		return nil, fmt.Errorf("failed to parse migration DSN: %w", err)
	}
	connCfg.DialFunc = tracker.dial

	db := sql.OpenDB(stdlib.GetConnector(*connCfg, stdlib.OptionAfterConnect(tracker.afterConnect)))
	driver, err := migratepgx.WithInstance(db, &migratepgx.Config{DatabaseName: u.Path})
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	src, err := (&file.File{}).Open("file://" + strings.TrimPrefix(migrationsPath, "file://"))
	if err != nil {
		_ = driver.Close()
		return nil, err
	}
	m, err := migrate.NewWithInstance("file", src, "pgx5", driver)
	if err != nil {
		_ = src.Close()
		_ = driver.Close()
		return nil, err
	}
	return m, nil
}

// wrapContextError makes a failure caused by ctx ending match errors.Is
// against ctx.Err(), so callers can tell a timeout from a migration failure.
func wrapContextError(ctx context.Context, err error) error {
	if err == nil || ctx.Err() == nil {
		return err
	}
	return fmt.Errorf("migrations interrupted: %w: %w", ctx.Err(), err)
}
