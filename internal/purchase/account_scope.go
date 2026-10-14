package purchase

import (
	"fmt"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// scopeExecutionsByAccount splits a plan step's recommendations across the
// plan's accounts so each account buys only its own (platform#631). The
// returned executions are per-account copies of base whose Recommendations
// and money totals cover only that account's recs; accounts with no recs of
// their own are omitted.
//
// It fails loudly rather than dropping anything: a rec with no account, or an
// account outside the plan, would otherwise be silently unbought (or, before
// this scoping existed, bought by every account). An empty step returns
// ErrPlanStepNoRecommendations so it cannot complete and advance the ramp.
func scopeExecutionsByAccount(base *config.PurchaseExecution, accounts []config.CloudAccount) (map[string]*config.PurchaseExecution, error) {
	if len(base.Recommendations) == 0 {
		return nil, fmt.Errorf("execution %s: %w", base.ExecutionID, ErrPlanStepNoRecommendations)
	}
	planAccounts := make(map[string]bool, len(accounts))
	for i := range accounts {
		planAccounts[accounts[i].ID] = true
	}
	byAccount := make(map[string][]config.RecommendationRecord, len(accounts))
	var stray []string
	for i := range base.Recommendations {
		rec := base.Recommendations[i]
		if rec.CloudAccountID == nil || !planAccounts[*rec.CloudAccountID] {
			stray = append(stray, describeScopeRec(&rec))
			continue
		}
		byAccount[*rec.CloudAccountID] = append(byAccount[*rec.CloudAccountID], rec)
	}
	if len(stray) > 0 {
		return nil, fmt.Errorf("execution %s: %d recommendation(s) are not attributed to one of the plan's accounts and cannot be bought: %s",
			base.ExecutionID, len(stray), strings.Join(stray, ", "))
	}

	scoped := make(map[string]*config.PurchaseExecution, len(byAccount))
	for id, recs := range byAccount {
		e := *base
		e.Recommendations = recs
		e.TotalUpfrontCost, e.EstimatedSavings = 0, 0
		for i := range recs {
			e.TotalUpfrontCost += recs[i].UpfrontCost
			e.EstimatedSavings += recs[i].Savings
		}
		scoped[id] = &e
	}
	return scoped, nil
}

func describeScopeRec(rec *config.RecommendationRecord) string {
	acct := "no account"
	if rec.CloudAccountID != nil {
		acct = "account " + *rec.CloudAccountID
	}
	return fmt.Sprintf("%s/%s %s (%s)", rec.Provider, rec.Service, rec.ResourceType, acct)
}

// requireRecsMatchRowAccount is the single-account path's version of the
// fan-out scoping: a plan row bound to an account may only buy recs attributed
// to that same account (strict equality; a rec with no account fails). Direct
// executes (no plan) and unscoped plan rows keep their existing resolution.
func requireRecsMatchRowAccount(exec *config.PurchaseExecution) error {
	if exec.PlanID == "" || exec.CloudAccountID == nil {
		return nil
	}
	for i := range exec.Recommendations {
		rec := exec.Recommendations[i]
		if !rec.Selected {
			continue
		}
		if rec.CloudAccountID == nil || *rec.CloudAccountID != *exec.CloudAccountID {
			return fmt.Errorf("execution %s is bound to account %s but recommendation %s is not attributed to it",
				exec.ExecutionID, *exec.CloudAccountID, describeScopeRec(&rec))
		}
	}
	return nil
}
