package purchase

import (
	"errors"
	"strings"
	"sync"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// A late account is one that finished after the run context ended (issue
// #1672). It already saved its own row, on its slice of the terminal window,
// but it sends no email of its own: the run's single batched email goes out
// after the root save, so the failure alerts of a fan-out cannot stack up past
// the Lambda budget. The row travels back to executeAndFinalize on the error
// executeMultiAccount returns.

// lateRows collects the late accounts' rows from the fan-out goroutines.
type lateRows struct {
	mu   sync.Mutex
	rows []*config.PurchaseExecution
}

// add keeps a copy of row when it ended failed or partially_completed, the only
// statuses that email, and reports whether it did. A nil collector keeps nothing.
func (l *lateRows) add(row *config.PurchaseExecution) bool {
	if l == nil || (row.Status != "failed" && row.Status != "partially_completed") {
		return false
	}
	cp := *row
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rows = append(l.rows, &cp)
	return true
}

func (l *lateRows) take() []*config.PurchaseExecution {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rows
}

// lateAccountsError carries the late rows alongside the fan-out's own error.
// It is transparent: same message, and errors.Is/As reach the wrapped error.
type lateAccountsError struct {
	err  error
	rows []*config.PurchaseExecution
}

func (e *lateAccountsError) Error() string { return e.err.Error() }
func (e *lateAccountsError) Unwrap() error { return e.err }

func withLateRows(err error, rows []*config.PurchaseExecution) error {
	if err == nil || len(rows) == 0 {
		return err
	}
	return &lateAccountsError{err: err, rows: rows}
}

func lateRowsOf(err error) []*config.PurchaseExecution {
	var late *lateAccountsError
	if errors.As(err, &late) {
		return late.rows
	}
	return nil
}

// hasOutcomeUnknownRec reports whether a rec of row failed with the go
// library's ErrOutcomeUnknown: its purchase call may have reached the cloud, so
// the commitment may exist. The rec stores only the error text.
func hasOutcomeUnknownRec(row *config.PurchaseExecution) bool {
	for i := range row.Recommendations {
		if strings.Contains(row.Recommendations[i].Error, common.ErrOutcomeUnknown.Error()) {
			return true
		}
	}
	return false
}
