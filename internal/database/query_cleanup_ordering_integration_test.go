//go:build integration

package database_test

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/puddle/v2"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
)

type queryCloseEvent struct {
	pool *pgxpool.Pool
	at   time.Time
}

type queryCloseObserver struct {
	events   chan queryCloseEvent
	overflow atomic.Bool
}

func (o *queryCloseObserver) publish(pool *pgxpool.Pool) {
	if o == nil {
		return
	}
	select {
	case o.events <- queryCloseEvent{pool, time.Now()}:
	default:
		o.overflow.Store(true)
	}
}

type queryTerminateEvent struct {
	completed bool
	at        time.Time
	err       error
}

type queryTerminationObserver struct {
	testcontainers.Container
	events   chan queryTerminateEvent
	overflow atomic.Bool
}

func (o *queryTerminationObserver) publish(event queryTerminateEvent) {
	select {
	case o.events <- event:
	default:
		o.overflow.Store(true)
	}
}

func (o *queryTerminationObserver) Terminate(ctx context.Context, options ...testcontainers.TerminateOption) error {
	o.publish(queryTerminateEvent{at: time.Now()})
	err := o.Container.Terminate(ctx, options...)
	o.publish(queryTerminateEvent{completed: true, at: time.Now(), err: err})
	return err
}

func runQueryDiagnosticsHeldLease(t *testing.T) {
	overall, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	overallDeadline, _ := overall.Deadline()
	productive, stopProductive := context.WithDeadline(overall, overallDeadline.Add(-60*time.Second))
	defer stopProductive()
	setup, stopSetup := context.WithTimeout(productive, 120*time.Second)
	defer stopSetup()
	fixture := testhelpers.RequirePostgresContainer(setup, t)
	pool := fixture.DB.Pool()
	termination := &queryTerminationObserver{Container: fixture.Container, events: make(chan queryTerminateEvent, 2)}
	fixture.Container = termination
	closeSite := &queryCloseObserver{events: make(chan queryCloseEvent, 1)}
	starts := make(chan time.Time, 1)
	type returnedCleanup struct {
		value queryDiagnosticsCleanup
		at    time.Time
	}
	results := make(chan returnedCleanup, 1)
	var workerOverflow atomic.Bool
	var held *pgxpool.Conn
	var releaseStarted, releasedAt time.Time
	release := func() {
		if held != nil {
			releaseStarted = time.Now()
			held.Release()
			held = nil
			releasedAt = time.Now()
		}
	}
	started, haveResult, finished := false, false, false
	var result returnedCleanup
	var resultObservedAt time.Time
	var hardD time.Time
	start := func() {
		if started {
			return
		}
		started = true
		hardD = queryDiagnosticsDeadline(overallDeadline, 55*time.Second)
		go func() {
			t0 := time.Now()
			select {
			case starts <- t0:
			default:
				workerOverflow.Store(true)
			}
			value := beginQueryDiagnosticsCleanup(fixture, []*pgxpool.Pool{pool}, "du-held-lease", overallDeadline, closeSite)
			select {
			case results <- returnedCleanup{value, time.Now()}:
			default:
				workerOverflow.Store(true)
			}
		}()
	}
	var entryAt, completedAt time.Time
	var terminateErr error
	entries, completions := 0, 0
	recordTermination := func(event queryTerminateEvent) {
		if event.completed {
			completions++
			completedAt, terminateErr = event.at, event.err
		} else {
			entries++
			entryAt = event.at
		}
	}
	resourcesJoined := false
	finish := func() {
		if finished {
			return
		}
		finished = true
		release()
		start()
		join, stop := context.WithDeadline(context.Background(), hardD)
		defer stop()
		if !haveResult {
			select {
			case result = <-results:
				haveResult = true
				resultObservedAt = time.Now()
			case <-join.Done():
				t.Error("query cleanup helper join inconclusive")
				return
			}
		}
		for _, done := range result.value.closes {
			select {
			case <-done:
			case <-join.Done():
				t.Error("query native Close join inconclusive")
				return
			}
		}
		draining := true
		for draining {
			select {
			case event := <-termination.events:
				recordTermination(event)
			default:
				draining = false
			}
		}
		resourcesJoined = len(result.value.closes) == 1 && result.value.reconcile == nil && result.value.terminated && result.value.termination == nil && entries == 1 && completions == 1 && terminateErr == nil && !time.Now().After(hardD)
		t.Logf("query actual cleanup: reconcile=%v termination=%v joined=%v entry=%v completion=%v released=%v D=%v", result.value.reconcile, result.value.termination, resourcesJoined, entryAt, completedAt, releasedAt, hardD)
		if !resourcesJoined {
			t.Error("query owned cleanup incomplete/inconclusive")
		}
	}
	t.Cleanup(finish)
	// The dependency Background health probe can overrun the ctx-aware setup target.
	require.NoError(t, productive.Err(), "productive reserve missed during setup; inconclusive")
	_, source, _, _ := runtime.Caller(0)
	require.NoError(t, migrations.RunMigrations(setup, pool, filepath.Join(filepath.Dir(source), "postgres", "migrations"), "", ""))
	require.GreaterOrEqual(t, time.Until(overallDeadline), 85*time.Second, "insufficient original Acquire/control reserve; inconclusive")
	acquire, stopAcquire := context.WithTimeout(productive, 10*time.Second)
	var err error
	held, err = pool.Acquire(acquire)
	stopAcquire()
	require.NoError(t, err)
	require.GreaterOrEqual(t, time.Until(overallDeadline), 75*time.Second, "insufficient original control reserve; inconclusive")
	require.NoError(t, productive.Err())
	_, err = held.Exec(productive, `SET application_name = 'du-held-lease'`)
	require.NoError(t, err)
	require.GreaterOrEqual(t, time.Until(overallDeadline), 75*time.Second, "control reserve consumed before cleanup invocation; inconclusive")
	start()
	initialR := hardD.Add(-50 * time.Second)
	startCtx, stopStart := context.WithDeadline(context.Background(), initialR.Add(-time.Second))
	var t0 time.Time
	select {
	case t0 = <-starts:
	case <-startCtx.Done():
	}
	stopStart()
	startObservedAt := time.Now()
	startValid := !t0.IsZero() && !startObservedAt.After(initialR.Add(-time.Second))
	if startValid {
		hardD = t0.Add(55 * time.Second)
		if overallDeadline.Before(hardD) {
			hardD = overallDeadline
		}
	}
	hardR := hardD.Add(-50 * time.Second)
	cutoff := hardR.Add(-time.Second)
	var site queryCloseEvent
	var witnessAt time.Time
	witness := false
	if startValid && time.Now().Before(cutoff) {
		observation, stop := context.WithDeadline(context.Background(), cutoff)
		select {
		case site = <-closeSite.events:
		case <-observation.Done():
		}
		if site.pool == pool && time.Now().Before(cutoff) {
			probe, probeErr := pool.Acquire(observation)
			witnessAt = time.Now()
			if probe != nil {
				probe.Release()
			}
			witness = errors.Is(probeErr, puddle.ErrClosedPool) && !witnessAt.After(cutoff) && held != nil
		}
		stop()
	}
	if witness {
		observation, stop := context.WithDeadline(context.Background(), cutoff)
		waiting := true
		for waiting {
			select {
			case event := <-termination.events:
				recordTermination(event)
				waiting = completions == 0
			case <-observation.Done():
				waiting = false
			}
		}
		stop()
	}
	positive := false
	releaseCeiling := hardR
	if witness && completions == 1 && terminateErr == nil && !completedAt.After(hardR) && !time.Now().After(cutoff) {
		hardP := completedAt.Add(20 * time.Second)
		if hardD.Add(-20 * time.Second).Before(hardP) {
			hardP = hardD.Add(-20 * time.Second)
		}
		releaseCeiling = hardP
		observation, stop := context.WithDeadline(context.Background(), hardP.Add(-time.Second))
		if time.Now().Before(hardP.Add(-time.Second)) {
			select {
			case result = <-results:
				haveResult = true
				resultObservedAt = time.Now()
			case <-observation.Done():
			}
		}
		stop()
		if haveResult && !resultObservedAt.After(hardP.Add(-time.Second)) && !time.Now().After(hardP.Add(-time.Second)) && len(result.value.closes) == 1 {
			select {
			case <-result.value.closes[0]:
			default:
				positive = result.value.terminated && result.value.termination == nil && !result.value.clean()
			}
		}
	}
	release()
	finish()
	require.True(t, resourcesJoined, "query cleanup reconciliation inconclusive")
	require.False(t, workerOverflow.Load() || closeSite.overflow.Load() || termination.overflow.Load(), "query cleanup observation overflow; inconclusive")
	require.Zero(t, len(closeSite.events)+len(starts)+len(results), "query unaccounted observations; inconclusive")
	require.True(t, startValid && !releasedAt.After(releaseCeiling), "query missed actual release ceiling; inconclusive")
	require.True(t, witness && site.pool == pool && !site.at.After(witnessAt) && witnessAt.Before(releaseStarted), "query native Close entry not witnessed while exact lease held; inconclusive")
	causalMutant := !entryAt.Before(releasedAt) && !completedAt.Before(releasedAt) && !releasedAt.After(hardR)
	if !positive && !causalMutant {
		t.Fatal("query ordering phase inconclusive; no eligible semantic RED")
	}
	t.Logf("query ordering evidence: t0=%v start_observed=%v Close_site=%v ErrClosedPool=%v helper_return=%v result_observed=%v Release_start=%v Release_done=%v R=%v ceiling=%v", t0, startObservedAt, site.at, witnessAt, result.at, resultObservedAt, releaseStarted, releasedAt, hardR, releaseCeiling)
	require.True(t, positive, "query-cleanup-termination-waited-for-lease")
}
