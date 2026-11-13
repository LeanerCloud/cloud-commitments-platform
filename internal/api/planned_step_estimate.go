package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/planstep"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/aws/aws-lambda-go/events"
	"github.com/jackc/pgx/v5"
)

// stepEstimator fills the figures of the next unattached plan step from the
// current recommendations, through the same planstep.Resolve the scheduler
// attaches with. Nothing it computes is stored, and approval, the cap and
// retry never read it: they read the attached execution. It is shown only to
// callers who may view recommendations, because it exposes their pricing.
type stepEstimator struct {
	h     *Handler
	plans map[string]planEstimate
}

// planEstimate is one plan's next-step estimate, computed once per request.
type planEstimate struct {
	recs []config.RecommendationRecord
	note string
}

// newStepEstimator returns nil (no estimates) for a caller without
// view:recommendations; any other permission failure is returned.
func (h *Handler) newStepEstimator(ctx context.Context, req *events.LambdaFunctionURLRequest) (*stepEstimator, error) {
	if _, err := h.requirePermission(ctx, req, "view", "recommendations"); err != nil {
		if isForbidden(err) {
			return nil, nil
		}
		return nil, err
	}
	return &stepEstimator{h: h, plans: map[string]planEstimate{}}, nil
}

// fill sets the estimate fields of a row with no attached recommendations.
func (e *stepEstimator) fill(ctx context.Context, row *PlannedPurchase, plan *config.PurchasePlan, exec *config.PurchaseExecution) {
	if e == nil || len(exec.Recommendations) > 0 {
		return
	}
	next := plan.RampSchedule.CurrentStep + 1
	switch {
	case exec.StepNumber < next:
		return
	case exec.StepNumber > next:
		row.EstimateNote = fmt.Sprintf("sized when step %d runs", exec.StepNumber-1)
		return
	}
	est := e.forPlan(ctx, plan, next)
	row.EstimateNote = est.note
	if len(est.recs) == 0 {
		return
	}
	row.Estimate = true
	summarizePlannedRecs(row, est.recs)
	for i := range est.recs {
		row.UpfrontCost += est.recs[i].UpfrontCost
		row.EstimatedSavings += est.recs[i].Savings
	}
}

func (e *stepEstimator) forPlan(ctx context.Context, plan *config.PurchasePlan, step int) planEstimate {
	if est, ok := e.plans[plan.ID]; ok {
		return est
	}
	est := e.resolve(ctx, plan, step)
	e.plans[plan.ID] = est
	return est
}

// resolve mirrors attach without its locks or writes (the step-1 baseline is
// never frozen); every outcome is figures or a note, never zeros.
func (e *stepEstimator) resolve(ctx context.Context, plan *config.PurchasePlan, step int) planEstimate {
	h := e.h
	accounts, err := h.config.GetPlanAccounts(ctx, plan.ID)
	if err != nil {
		return e.unavailable(plan, err)
	}
	ids := make([]string, 0, len(accounts))
	for i := range accounts {
		ids = append(ids, accounts[i].ID)
	}
	var baseline config.PlanRampBaseline
	var committed map[string]config.PlanRampQuantity
	err = h.config.WithTx(ctx, func(tx pgx.Tx) error {
		var txErr error
		if baseline, txErr = h.config.GetPlanRampBaselineTx(ctx, tx, plan.ID); txErr != nil {
			return txErr
		}
		committed, txErr = h.config.ListPlanCommittedTx(ctx, tx, plan.ID)
		return txErr
	})
	if err != nil {
		return e.estimateErr(plan, err)
	}
	res, err := planstep.Resolve(ctx, h.scheduler, h.config, plan, ids, step, baseline, committed)
	if err != nil {
		return e.estimateErr(plan, err)
	}
	if purchase.StepCapApplies(plan) {
		if capErr := purchase.CheckStepCommitmentCap(plan, res.Recs); capErr != nil {
			return planEstimate{note: capErr.Error()}
		}
	}
	return planEstimate{recs: res.Recs}
}

func (e *stepEstimator) estimateErr(plan *config.PurchasePlan, err error) planEstimate {
	switch {
	case errors.Is(err, planstep.ErrNoMatch):
		return planEstimate{note: "no matching recommendation right now"}
	case errors.Is(err, planstep.ErrTargetMet):
		return planEstimate{note: "target already met"}
	case errors.Is(err, planstep.ErrUnsupportedPlan), errors.Is(err, config.ErrRampLedgerUntrusted):
		return planEstimate{note: err.Error()}
	}
	return e.unavailable(plan, err)
}

func (e *stepEstimator) unavailable(plan *config.PurchasePlan, err error) planEstimate {
	logging.Warnf("planned purchases: estimate for plan %s failed: %v", plan.ID, err)
	return planEstimate{note: "estimate unavailable"}
}
