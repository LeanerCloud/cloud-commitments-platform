package purchase

import (
	"errors"
	"fmt"
	"sync"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/execution"
)

// failureTally counts one aggregation's failed recs and how many of them were
// refused before any purchase call. Per-rec errors reach the callers only as
// strings, so the beforeSubmission class has to be read here, while the error
// values are still in hand.
type failureTally struct{ failed, refused int }

// add records one failed rec. A rec counts as refused only when its error
// carries the beforeSubmission class and no unknown outcome: an error that
// also wraps ErrOutcomeUnknown may have reached the cloud.
func (t *failureTally) add(err error) {
	t.failed++
	if refusedBeforeSubmission(err) {
		t.refused++
	}
}

// refusedBeforeSubmission reports an error that proves nothing reached the
// cloud: the beforeSubmission class without an unknown outcome.
func refusedBeforeSubmission(err error) bool {
	var notSubmitted *beforeSubmissionError
	return errors.As(err, &notSubmitted) && !errors.Is(err, common.ErrOutcomeUnknown)
}

// fanOutNotSubmitted reports that a fan-out where no account committed provably
// submitted nothing, so the root may carry the no_submission proof and release
// its suppressions. It reads the in-memory results, not saved rows: a panicked
// account leaves no row and a lost row would read as "nothing submitted". Every
// account must be refused before submission, never started (the run context
// ended first, as skippedAccountRows decides), or have bought nothing without
// error; any other ending (panic, plain error, unknown outcome, a bought rec)
// may have reached the cloud, so it keeps today's unproven failure.
func fanOutNotSubmitted(results []execution.Result[bool], started *sync.Map) bool {
	if len(results) == 0 {
		return false
	}
	for _, r := range results {
		if r.Value {
			return false
		}
		if r.Err == nil || refusedBeforeSubmission(r.Err) {
			continue
		}
		if _, ran := started.Load(r.AccountID); ran {
			return false
		}
	}
	return true
}

// allRefused reports that every failed rec was refused before submission and
// no rec holds a commitment, so the whole row proves nothing was submitted.
func (t failureTally) allRefused(recs []config.RecommendationRecord) bool {
	return t.failed > 0 && t.failed == t.refused && !anyRecPurchased(recs)
}

// failedPurchasesError is the "some purchases failed" error of a run where
// nothing committed. When every failure was a pre-submission refusal it carries
// the beforeSubmission class, so saveTerminalExecution records no_submission
// and releases the row's suppressions.
func failedPurchasesError(purchaseErrors []string, allRefused bool) error {
	err := fmt.Errorf("some purchases failed: %v", purchaseErrors)
	if allRefused {
		return beforeSubmission(err)
	}
	return err
}

// markRefusedAccountRow stamps NoSubmission on a failed per-account row whose
// every rec was refused before submission. The fan-out saves that row itself,
// bypassing saveTerminalExecution.
func markRefusedAccountRow(row *config.PurchaseExecution, allRefused bool) {
	if allRefused && row.Status == config.StatusFailed {
		row.NoSubmission = true
	}
}
