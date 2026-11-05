package purchase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
)

// redshiftRedriveHorizon is how long after a lineage's first dispatch a
// successor may still be stamped as within horizon.
const redshiftRedriveHorizon = 24 * time.Hour

// isRedshiftRec reports whether rec dispatches to the AWS Redshift purchase
// path. It goes through mapServiceSlug, the table the executor dispatches on.
func isRedshiftRec(rec config.RecommendationRecord) bool {
	if rec.Provider != "" && rec.Provider != string(common.ProviderAWS) {
		return false
	}
	svc, ok := mapServiceSlug(rec.Service)
	return ok && (svc == common.ServiceRedshift || svc == common.ServiceDataWarehouse)
}

func hasRedshiftRec(exec *config.PurchaseExecution) bool {
	for i := range exec.Recommendations {
		if exec.Recommendations[i].Selected && isRedshiftRec(exec.Recommendations[i]) {
			return true
		}
	}
	return false
}

// afterRedshiftStampHook runs inside the gate transaction right after the
// stamp. Tests set it to observe the lock wait; it is nil in production.
var afterRedshiftStampHook func()

// errRedshiftPastHorizon marks a stamp older than the horizon. It is never
// beforeSubmission: an old stamp proves an earlier dispatch may have landed.
var errRedshiftPastHorizon = errors.New("outside the re-drive horizon")

// redshiftTuples lists the tuples of the selected Redshift recs.
func redshiftTuples(exec *config.PurchaseExecution) []config.RedshiftTuple {
	var tuples []config.RedshiftTuple
	for i := range exec.Recommendations {
		r := &exec.Recommendations[i]
		if r.Selected && isRedshiftRec(*r) {
			tuples = append(tuples, config.RedshiftTuple{
				Region: r.Region, NodeType: r.ResourceType, Term: r.Term,
				Payment: r.Payment, Count: r.Count, CloudAccountID: r.CloudAccountID,
			})
		}
	}
	return tuples
}

// stampRedshiftDispatch is the dispatch gate for a Redshift execution. In one
// transaction it locks each tuple, stamps the lineage's first dispatch (which
// becomes RequestedAt for every dispatch and fan-out account row), and refuses
// when another open lineage holds a matching tuple; the refusal rolls the
// stamp back. Other executions pass untouched. A refusal at attempt 0 or by a
// conflict provably submitted nothing; a stamp-less or past-horizon successor
// may have, so that refusal is a plain error.
// The gate needs READ COMMITTED: each statement sees a fresh snapshot after the
// lock wait. now() is the tx start, so a waiter's stamp predates its wait.
func (m *Manager) stampRedshiftDispatch(ctx context.Context, exec *config.PurchaseExecution) error {
	if !hasRedshiftRec(exec) {
		return nil
	}
	var res config.StampResult
	err := m.config.WithTx(ctx, func(tx pgx.Tx) error {
		var gateErr error
		res, gateErr = m.redshiftGate(ctx, tx, exec)
		return gateErr
	})
	if err != nil {
		var refused *beforeSubmissionError
		if exec.RetryAttemptN == 0 && !errors.As(err, &refused) && !errors.Is(err, errRedshiftPastHorizon) {
			return beforeSubmission(err)
		}
		return err
	}
	at := res.At.UTC()
	exec.FirstDispatchAt = &at
	return nil
}

func (m *Manager) redshiftGate(ctx context.Context, tx pgx.Tx, exec *config.PurchaseExecution) (config.StampResult, error) {
	tuples := redshiftTuples(exec)
	if err := m.config.LockRedshiftTuplesTx(ctx, tx, tuples); err != nil {
		return config.StampResult{}, err
	}
	res, err := m.config.StampFirstDispatchTx(ctx, tx, exec.ExecutionID, redshiftRedriveHorizon)
	if err != nil {
		return res, err
	}
	if res.At == nil {
		return res, fmt.Errorf("execution %s has no first dispatch time and none can be minted", exec.ExecutionID)
	}
	if !res.WithinHorizon {
		return res, fmt.Errorf("execution %s first dispatched at %s: %w", exec.ExecutionID, res.At.UTC().Format(time.RFC3339), errRedshiftPastHorizon)
	}
	if afterRedshiftStampHook != nil {
		afterRedshiftStampHook()
	}
	family, _, _ := strings.Cut(idempotencyLineageKey(exec), ":")
	conflicts, err := m.config.FindOpenRedshiftLineageConflictsTx(ctx, tx, exec.ExecutionID, family, tuples, exec.CloudAccountID, redshiftRedriveHorizon)
	if err != nil {
		return res, err
	}
	if len(conflicts) > 0 {
		return res, beforeSubmission(fmt.Errorf("another open purchase (execution %s, %s) holds a matching Redshift node; this one was not sent", conflicts[0].ExecutionID, conflicts[0].Status))
	}
	return res, nil
}

// withDispatchTime defaults RequestedAt to now. A Redshift rec must arrive
// with the lineage stamp: defaulting would silently move go's dedupe window.
func withDispatchTime(rec config.RecommendationRecord, opts common.PurchaseOptions) (common.PurchaseOptions, error) {
	if !opts.RequestedAt.IsZero() {
		return opts, nil
	}
	if isRedshiftRec(rec) {
		return opts, fmt.Errorf("purchase[%s]: Redshift dispatch without a first dispatch time", opts.ExecutionID)
	}
	opts.RequestedAt = time.Now().UTC()
	return opts, nil
}
