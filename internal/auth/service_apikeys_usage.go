package auth

import (
	"context"
	"sync/atomic"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
)

// RecordUsageAsync books one request against keyID and flushes the pending
// count to the store off the authentication hot path.
//
// Callers must invoke this exactly once per inbound request, not once per
// credential validation -- a single request validates the same key several
// times over. The API layer owns that guarantee (see
// Handler.validateSecurityContext).
//
// The count is accumulated in memory first and a flush writes the whole
// accumulated delta, so requests that arrive while a write is in flight are
// carried by the next write instead of being dropped.
//
// At most one flush goroutine runs per keyID at any moment, which bounds both
// the write rate (one in-flight DB write per key) and the goroutine count: a
// request whose key already has a flusher only books its count and returns,
// and that flusher picks the count up (issue #104; earlier, every request
// spawned a goroutine that then parked in a singleflight.Group).
func (s *Service) RecordUsageAsync(keyID string) {
	// Book the request BEFORE trying to start a flusher, so it can never be
	// missed by a flusher that is already draining the counter.
	entry := s.addPendingUsage(keyID)
	if !entry.flushing.CompareAndSwap(false, true) {
		return
	}
	go s.runUsageFlusher(keyID, entry)
}

// runUsageFlusher writes keyID's pending count until none is left, then
// releases the key's flusher slot. The caller has set entry.flushing.
func (s *Service) runUsageFlusher(keyID string, entry *apiKeyUsageEntry) {
	defer func() {
		if r := recover(); r != nil {
			entry.flushing.Store(false)
			logging.Warnf("service_apikeys: RecordUsage flush panic: %v", r)
		}
	}()
	for {
		for entry.count.Load() != 0 {
			s.flushPendingUsage(keyID)
		}
		if !entry.releaseFlusher() {
			return
		}
	}
}

// apiKeyUsageEntry is one API key's not-yet-flushed request count and whether
// a flush goroutine currently owns the key.
type apiKeyUsageEntry struct {
	count    atomic.Int64
	flushing atomic.Bool
}

// releaseFlusher gives up the key's flusher slot after a drain. It reports
// true, with the slot taken again, when there is a count to write that no
// other flusher will: a request booked after the last drain may have seen the
// slot still taken and returned without starting a flusher. The
// CompareAndSwap keeps a single flusher if a new request started one in the
// meantime. Go atomics are sequentially consistent, so a request whose
// CompareAndSwap failed booked its count before the Store(false) below, and
// the Load that follows sees it.
func (e *apiKeyUsageEntry) releaseFlusher() bool {
	e.flushing.Store(false)
	return e.count.Load() != 0 && e.flushing.CompareAndSwap(false, true)
}

// addPendingUsage books one not-yet-flushed request against keyID and
// returns the key's entry.
//
// Entries are never removed: an entry is two atomics and the number of
// distinct keys is bounded by the number of API keys that exist, so this is
// a fixed small cost rather than unbounded growth. Removing entries would
// race with in-flight increments and risk losing counts.
func (s *Service) addPendingUsage(keyID string) *apiKeyUsageEntry {
	s.pendingUsageMu.Lock()
	if s.pendingUsage == nil {
		s.pendingUsage = make(map[string]*apiKeyUsageEntry)
	}
	entry, ok := s.pendingUsage[keyID]
	if !ok {
		entry = new(apiKeyUsageEntry)
		s.pendingUsage[keyID] = entry
	}
	s.pendingUsageMu.Unlock()

	entry.count.Add(1)
	return entry
}

// pendingEntry returns the entry for keyID, or nil if the key has never
// booked a request. The mutex covers only the map lookup; reads and writes
// of the returned entry are atomic.
func (s *Service) pendingEntry(keyID string) *apiKeyUsageEntry {
	s.pendingUsageMu.Lock()
	defer s.pendingUsageMu.Unlock()
	return s.pendingUsage[keyID]
}

// flushPendingUsage writes the count accumulated for keyID so far to the
// store. A no-op when nothing is pending.
//
// On a store error the drained count is lost rather than retried: the caller
// is the key's background flusher on the auth path, and retrying a usage
// counter is not worth holding a database connection during an outage. The
// loss is logged and bounded to the requests in that one flush.
func (s *Service) flushPendingUsage(keyID string) {
	delta := s.drainPendingUsage(keyID)
	if delta == 0 {
		return
	}
	updateCtx, cancel := context.WithTimeout(context.Background(), apiKeyUsageFlushTimeout)
	defer cancel()
	if err := s.RecordUsage(updateCtx, keyID, delta); err != nil {
		logging.Debugf("Failed to record %d API key usage(s) for key %s: %v", delta, keyID, err)
	}
}

// drainPendingUsage atomically takes the pending count for keyID, resetting
// it to zero. Returns 0 when nothing is pending.
func (s *Service) drainPendingUsage(keyID string) int64 {
	entry := s.pendingEntry(keyID)
	if entry == nil {
		return 0
	}
	return entry.count.Swap(0)
}

// peekPendingUsage reports the pending count for keyID without consuming it.
func (s *Service) peekPendingUsage(keyID string) int64 {
	entry := s.pendingEntry(keyID)
	if entry == nil {
		return 0
	}
	return entry.count.Load()
}

// RecordUsage updates last_used_at and adds delta to both the lifetime
// counter and the fixed-window request counter for the key. delta is the
// number of requests being recorded, which is greater than 1 whenever
// concurrent requests were coalesced into one flush. See
// PostgresStore.RecordAPIKeyUsage for the atomic SQL and the window's
// tumbling-reset semantics.
func (s *Service) RecordUsage(ctx context.Context, keyID string, delta int64) error {
	return s.store.RecordAPIKeyUsage(ctx, keyID, delta)
}
