package purchase

import (
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
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
	var notSubmitted *beforeSubmissionError
	if errors.As(err, &notSubmitted) && !errors.Is(err, common.ErrOutcomeUnknown) {
		t.refused++
	}
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
