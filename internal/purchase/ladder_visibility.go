package purchase

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	sdksp "github.com/aws/aws-sdk-go-v2/service/savingsplans"
	sptypes "github.com/aws/aws-sdk-go-v2/service/savingsplans/types"
)

// ladderVisibleSPStates is the set of Savings Plan states a commitment must be
// in to count as visible. It MUST equal what the pinned AWS ladder passes to
// DescribeSavingsPlans when it builds ExistingUSDPerHour for GetLayerStates
// (providers/aws/ladder/adapters.go spListerStates, unexported, so it cannot be
// imported). TestLadderVisibleStatesMatchGetLayerStates pins the equivalence
// against the real AWSLadder. If this set were wider than the ladder's, a
// tranche would complete while netting still cannot see it and the next run
// would over-buy.
var ladderVisibleSPStates = []sptypes.SavingsPlanState{
	sptypes.SavingsPlanStateActive,
	sptypes.SavingsPlanStatePaymentPending,
}

// ladderDescribeBatch is how many plan IDs go into one DescribeSavingsPlans call.
const ladderDescribeBatch = 100

// SPDescriber is the one Savings Plans API call the visibility prefetch needs.
// *savingsplans.Client satisfies it.
type SPDescriber interface {
	DescribeSavingsPlans(ctx context.Context, in *sdksp.DescribeSavingsPlansInput, optFns ...func(*sdksp.Options)) (*sdksp.DescribeSavingsPlansOutput, error)
}

// LadderSPDescriberFactory returns a describer authenticated for one cloud
// account and region.
type LadderSPDescriberFactory func(ctx context.Context, cloudAccountID, region string) (SPDescriber, error)

// LadderSPDescriber is the production factory: the same per-account credential
// resolution the purchase itself used.
func (m *Manager) LadderSPDescriber(ctx context.Context, cloudAccountID, region string) (SPDescriber, error) {
	creds, err := m.ladderAWSCredentials(ctx, cloudAccountID)
	if err != nil {
		return nil, err
	}
	return sdksp.NewFromConfig(aws.Config{Region: region, Credentials: creds}), nil
}

type ladderVisibility struct {
	err     error
	visible bool
}

// prefetchedVisibility is a pure lookup over results fetched before the
// reconciler opens any transaction. Unknown executions fail closed.
type prefetchedVisibility map[string]ladderVisibility

func (p prefetchedVisibility) CommitmentVisible(_ context.Context, _ config.LadderTrancheDB, executionID string) (bool, error) {
	v, ok := p[executionID]
	if !ok {
		return false, fmt.Errorf("execution %s was not prefetched", executionID)
	}
	return v.visible, v.err
}

// expectedPlan is one commitment a completed execution should have created.
type expectedPlan struct {
	id, planType, region string
}

// spPlanTypeForLayer is the SavingsPlanType GetLayerStates sums into a layer.
func spPlanTypeForLayer(l ladder.LayerType) (sptypes.SavingsPlanType, error) {
	switch l {
	case ladder.LayerComputeSP:
		return sptypes.SavingsPlanTypeCompute, nil
	case ladder.LayerEC2InstanceSP:
		return sptypes.SavingsPlanTypeEc2Instance, nil
	}
	return "", fmt.Errorf("layer %q is not a savings plan layer", l)
}

// spCountsAsExisting mirrors the ladder's ExistingUSDPerHour predicate for one
// plan: state in ladderVisibleSPStates, plan type equal to the layer's, and an
// EC2Instance plan only in the ladder's region (Compute plans are global).
func spCountsAsExisting(sp sptypes.SavingsPlan, e expectedPlan) bool {
	return slices.Contains(ladderVisibleSPStates, sp.State) &&
		string(sp.SavingsPlanType) == e.planType &&
		(sp.SavingsPlanType != sptypes.SavingsPlanTypeEc2Instance || aws.ToString(sp.Region) == e.region)
}

// visibilityLookup fetches the commitments with the given IDs for one group and
// returns the predicate deciding whether an expected commitment counts as
// existing. Any error fails every execution in the group closed.
type visibilityLookup func(ctx context.Context, account, region string, ids []string) (func(expectedPlan) bool, error)

type ladderFetchGroup struct {
	execs           map[string][]expectedPlan // execution ID -> its plans
	lookup          visibilityLookup
	account, region string
}

// PrefetchLadderVisibility resolves, outside any transaction, whether the
// commitments of each fired tranche's completed execution are visible. It
// makes no provider call when nothing is fired or no execution has completed.
// Any lookup or API error is recorded against the affected executions and
// logged; nothing is ever reported visible on an error.
func PrefetchLadderVisibility(ctx context.Context, store interface {
	GetExecutionByID(ctx context.Context, id string) (*config.PurchaseExecution, error)
}, spFactory LadderSPDescriberFactory, riFactory LadderRIDescriberFactory, fired []config.LadderTrancheDB) prefetchedVisibility {
	out := prefetchedVisibility{}
	lookups := ladderLookups{sp: spLookup(spFactory), ri: riLookup(riFactory)}
	groups := map[string]*ladderFetchGroup{}
	for i := range fired {
		tr := fired[i]
		if tr.ExecutionID == nil {
			continue
		}
		exec, err := chainTail(ctx, store, *tr.ExecutionID)
		if err == nil && exec != nil && exec.Status == config.StatusCompleted {
			err = addExpected(groups, lookups, exec, tr)
		}
		if err != nil {
			logging.Errorf("ladder tranche %s: visibility prefetch failed: %v", tr.ID, err)
			if exec != nil {
				out[exec.ExecutionID] = ladderVisibility{err: err}
			}
		}
	}
	for _, g := range groups {
		fetchGroup(ctx, g, out)
	}
	return out
}

func chainTail(ctx context.Context, store interface {
	GetExecutionByID(ctx context.Context, id string) (*config.PurchaseExecution, error)
}, id string) (*config.PurchaseExecution, error) {
	var exec *config.PurchaseExecution
	for range config.LadderChainMaxDepth {
		var err error
		if exec, err = store.GetExecutionByID(ctx, id); err != nil {
			return nil, err
		}
		if exec == nil {
			return nil, fmt.Errorf("execution %s not found", id)
		}
		if exec.RetryExecutionID == nil {
			return exec, nil
		}
		id = *exec.RetryExecutionID
	}
	return exec, fmt.Errorf("retry chain longer than %d", config.LadderChainMaxDepth)
}

func ladderGroupKind(layer ladder.LayerType) string {
	if layer == ladder.LayerConvertibleRI {
		return "ri"
	}
	return "sp"
}

type ladderLookups struct{ sp, ri visibilityLookup }

// lookupForLayer dispatches by layer: the convertible RI layer is looked up as
// reservations, the Savings Plan layers as plans, any other layer fails closed.
func (l ladderLookups) lookupForLayer(layer ladder.LayerType) (lookup visibilityLookup, planType string, err error) {
	if layer == ladder.LayerConvertibleRI {
		return l.ri, "", nil
	}
	t, err := spPlanTypeForLayer(layer)
	return l.sp, string(t), err
}

func addExpected(groups map[string]*ladderFetchGroup, lookups ladderLookups, exec *config.PurchaseExecution, tr config.LadderTrancheDB) error {
	lookup, planType, err := lookups.lookupForLayer(tr.LayerType)
	if err != nil {
		return err
	}
	account, err := SingleCloudAccountIDFromRecs(exec.Recommendations)
	if err != nil {
		return err
	}
	if exec.CloudAccountID != nil {
		account = exec.CloudAccountID
	}
	var plans []expectedPlan
	region := ""
	for i := range exec.Recommendations {
		r := &exec.Recommendations[i]
		if !r.Selected {
			continue
		}
		if r.PurchaseID == "" {
			return fmt.Errorf("completed execution %s has a selected recommendation without a commitment ID", exec.ExecutionID)
		}
		plans = append(plans, expectedPlan{id: r.PurchaseID, planType: planType, region: r.Region})
		region = r.Region
	}
	if account == nil || len(plans) == 0 {
		return fmt.Errorf("completed execution %s has no account or commitment to look up", exec.ExecutionID)
	}
	key := ladderGroupKind(tr.LayerType) + "/" + *account + "/" + region
	g := groups[key]
	if g == nil {
		g = &ladderFetchGroup{lookup: lookup, account: *account, region: region, execs: map[string][]expectedPlan{}}
		groups[key] = g
	}
	g.execs[exec.ExecutionID] = plans
	return nil
}

// fetchGroup issues one batched per-ID lookup per (kind, account, region) and
// marks an execution visible only when every one of its commitments is
// returned and satisfies the lookup's predicate.
func fetchGroup(ctx context.Context, g *ladderFetchGroup, out prefetchedVisibility) {
	var ids []string
	for _, plans := range g.execs {
		for _, p := range plans {
			if !slices.Contains(ids, p.id) {
				ids = append(ids, p.id)
			}
		}
	}
	counts, err := g.lookup(ctx, g.account, g.region, ids)
	if err != nil {
		logging.Errorf("ladder visibility lookup for account %s region %s failed: %v", g.account, g.region, err)
		for id := range g.execs {
			out[id] = ladderVisibility{err: err}
		}
		return
	}
	for id, plans := range g.execs {
		out[id] = ladderVisibility{visible: !slices.ContainsFunc(plans, func(p expectedPlan) bool { return !counts(p) })}
	}
}

// spLookup is the Savings Plans lookup: per-ID DescribeSavingsPlans, matched
// against spCountsAsExisting.
func spLookup(factory LadderSPDescriberFactory) visibilityLookup {
	return func(ctx context.Context, account, region string, ids []string) (func(expectedPlan) bool, error) {
		desc, err := factory(ctx, account, region)
		if err != nil {
			return nil, err
		}
		found := map[string]sptypes.SavingsPlan{}
		for chunk := range slices.Chunk(ids, ladderDescribeBatch) {
			if err := describeIDs(ctx, desc, chunk, found); err != nil {
				return nil, err
			}
		}
		return func(p expectedPlan) bool {
			sp, ok := found[p.id]
			return ok && spCountsAsExisting(sp, p)
		}, nil
	}
}

func describeIDs(ctx context.Context, desc SPDescriber, ids []string, found map[string]sptypes.SavingsPlan) error {
	var token *string
	seen := map[string]bool{}
	for {
		res, err := desc.DescribeSavingsPlans(ctx, &sdksp.DescribeSavingsPlansInput{
			SavingsPlanIds: ids, States: ladderVisibleSPStates, NextToken: token,
		})
		if err != nil {
			return fmt.Errorf("DescribeSavingsPlans: %w", err)
		}
		for i := range res.SavingsPlans {
			found[aws.ToString(res.SavingsPlans[i].SavingsPlanId)] = res.SavingsPlans[i]
		}
		tok := aws.ToString(res.NextToken)
		if tok == "" {
			return nil
		}
		if seen[tok] {
			return errors.New("DescribeSavingsPlans returned a repeated pagination token")
		}
		seen[tok] = true
		token = res.NextToken
	}
}

// LadderReconcileDeps is what one scheduled reconcile run needs.
type LadderReconcileDeps struct {
	Store LadderReconcileRunStore
	SP    LadderSPDescriberFactory
	RI    LadderRIDescriberFactory
	Now   func() time.Time
}

// LadderReconcileRunStore is what RunLadderReconcile needs from the store.
type LadderReconcileRunStore interface {
	LadderReconcileStore
	GetExecutionByID(ctx context.Context, id string) (*config.PurchaseExecution, error)
}

// RunLadderReconcile is the entry point the ladder_reconcile scheduled task
// runs: prefetch visibility, then ReconcileLadderTranches against the map. With no fired tranche it touches no provider API.
func RunLadderReconcile(ctx context.Context, d LadderReconcileDeps) (LadderReconcileResult, error) {
	var fired []config.LadderTrancheDB
	err := forEachFiredPage(ctx, d.Store, ladderReconcileBatch, func(page []config.LadderTrancheDB) error {
		fired = append(fired, page...)
		return nil
	})
	if err != nil {
		return LadderReconcileResult{}, err
	}
	if len(fired) == 0 {
		return LadderReconcileResult{}, nil
	}
	vis := PrefetchLadderVisibility(ctx, d.Store, d.SP, d.RI, fired)
	return ReconcileLadderTranches(ctx, d.Store, vis, d.Now())
}
