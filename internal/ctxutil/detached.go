// Package ctxutil holds small context helpers shared across bounded contexts.
// Keep it minimal: add a helper here only when more than one package needs it.
package ctxutil

import (
	"context"
	"time"
)

// DetachedWithTimeout returns a context that keeps ctx's values but drops its
// cancellation and deadline, bounded instead by a fresh timeout of d.
//
// Use it for work that must outlive the request or caller that started it,
// such as a provider call that commits money and the terminal write recording
// its outcome: a client disconnect or a per-request timeout must not cancel
// either midway. Always defer the returned CancelFunc.
func DetachedWithTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), d)
}

// DetachedWithDeadline is DetachedWithTimeout with an absolute deadline: ctx's
// values are kept, its cancellation and deadline are dropped, and the result
// ends at deadline. Use it when several detached phases must share one
// schedule anchored on an earlier deadline instead of each starting a fresh
// timeout from "now". A deadline already in the past yields a context that is
// already done. Always defer the returned CancelFunc.
func DetachedWithDeadline(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	return context.WithDeadline(context.WithoutCancel(ctx), deadline)
}
