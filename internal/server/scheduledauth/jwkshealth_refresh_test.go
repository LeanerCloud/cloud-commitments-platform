package scheduledauth

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestJWKSHealth_RefreshClearsHiccup(t *testing.T) {
	srv := sequencedJWKS(t, step500, stepOK)
	v, clock := clockedValidator(t, srv.URL)
	v.Warmup(context.Background())
	clock.Advance(11 * time.Second)
	got := v.JWKSHealth(context.Background(), true)
	if !got.OK {
		t.Fatalf("refresh should clear the hiccup in the same call, got %+v", got)
	}
	if hits := srv.hits.Load(); hits != 2 {
		t.Fatalf("expected 2 server hits, got %d", hits)
	}
}

func TestJWKSHealth_RefreshWaitIsBounded(t *testing.T) {
	srv := sequencedJWKS(t, step500, stepBlock)
	v, clock := clockedValidator(t, srv.URL)
	v.jwksWait = 100 * time.Millisecond
	v.Warmup(context.Background())
	clock.Advance(11 * time.Second)

	start := time.Now()
	got := v.JWKSHealth(context.Background(), true)
	if el := time.Since(start); el > time.Second {
		t.Fatalf("refresh wait not bounded: %v", el)
	}
	if got.OK {
		t.Fatalf("blocked probe must still report degraded, got %+v", got)
	}
	srv.Release()
	waitIdle(t, v)
	if got := v.JWKSHealth(context.Background(), false); !got.OK {
		t.Fatalf("late success must land for the next read, got %+v", got)
	}
}

func TestJWKSHealth_SingleFlightAndRateLimit(t *testing.T) {
	srv := sequencedJWKS(t, step500, step500)
	v, clock := clockedValidator(t, srv.URL)
	v.Warmup(context.Background())
	clock.Advance(11 * time.Second)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v.JWKSHealth(context.Background(), true)
		}()
	}
	wg.Wait()
	waitIdle(t, v)
	if hits := srv.hits.Load(); hits != 2 {
		t.Fatalf("50 concurrent refreshes must cause exactly one probe, hits=%d", hits)
	}

	clock.Advance(time.Second)
	v.JWKSHealth(context.Background(), true)
	waitIdle(t, v)
	if hits := srv.hits.Load(); hits != 2 {
		t.Fatalf("re-probe within 10s of the last probe, hits=%d", hits)
	}
}

func TestJWKSHealth_ConcurrentRefreshOneProbe(t *testing.T) {
	srv := sequencedJWKS(t, step500)
	v, clock := clockedValidator(t, srv.URL)
	v.Warmup(context.Background())
	clock.Advance(11 * time.Second)

	v.jwksMu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		v.JWKSHealth(context.Background(), true)
	}()
	v.storeJWKSResult(JWKSReasonHTTPError) // a probe finished meanwhile, at=now
	v.jwksMu.Unlock()
	<-done
	waitIdle(t, v)
	if hits := srv.hits.Load(); hits != 1 {
		t.Fatalf("blocked read must re-check the age after the lock, hits=%d", hits)
	}
}

func TestJWKSHealth_ZeroWaitFieldUsesDefault(t *testing.T) {
	srv := sequencedJWKS(t, step500, stepBlock)
	v, clock := clockedValidator(t, srv.URL)
	v.Warmup(context.Background())
	clock.Advance(11 * time.Second)
	orig := jwksRefreshWait
	jwksRefreshWait = 150 * time.Millisecond
	t.Cleanup(func() { jwksRefreshWait = orig })
	start := time.Now()
	v.JWKSHealth(context.Background(), true)
	if el := time.Since(start); el < 100*time.Millisecond || el > time.Second {
		t.Fatalf("zero jwksWait must wait the default bound, waited %v", el)
	}
}
