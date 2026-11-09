package purchase

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ecDupExec(recs ...config.RecommendationRecord) *config.PurchaseExecution {
	return &config.PurchaseExecution{
		ExecutionID: "ec-dup", IdempotencyKey: "ec-dup-lineage", Status: "running", Recommendations: recs,
	}
}

func TestElastiCacheDuplicatePool_Detection(t *testing.T) {
	base := func() config.RecommendationRecord { return ecRecWith(t, "redis", 2, "cache.r6g.large", ecRegion) }
	with := func(f func(*config.RecommendationRecord)) config.RecommendationRecord {
		r := base()
		f(&r)
		return r
	}
	unselected := with(func(r *config.RecommendationRecord) { r.Selected = false })
	memorydb := with(func(r *config.RecommendationRecord) { r.Service = "memorydb" })
	azure := with(func(r *config.RecommendationRecord) { r.Provider = "azure" })
	noAcct := withAccount(base(), "acct-A")
	noAcct.CloudAccountID = nil
	canonical := with(func(r *config.RecommendationRecord) { r.Service = "cache" })

	cases := map[string]struct {
		recs    []config.RecommendationRecord
		refused bool
	}{
		"identical":                     {[]config.RecommendationRecord{base(), base()}, true},
		"different count":               {[]config.RecommendationRecord{base(), with(func(r *config.RecommendationRecord) { r.Count = 5 })}, true},
		"different term and payment":    {[]config.RecommendationRecord{base(), with(func(r *config.RecommendationRecord) { r.Term, r.Payment = 3, "all-upfront" })}, true},
		"untrimmed mixed-case engine":   {[]config.RecommendationRecord{base(), ecRecWith(t, " Redis ", 2, "cache.r6g.large", ecRegion)}, true},
		"redis vs valkey":               {[]config.RecommendationRecord{base(), ecRecWith(t, "valkey", 2, "cache.r6g.large", ecRegion)}, false},
		"other account":                 {[]config.RecommendationRecord{withAccount(base(), "acct-A"), withAccount(base(), "acct-B")}, false},
		"other region":                  {[]config.RecommendationRecord{base(), with(func(r *config.RecommendationRecord) { r.Region = "eu-west-1" })}, false},
		"unselected twin":               {[]config.RecommendationRecord{base(), unselected}, false},
		"aws memorydb never counted":    {[]config.RecommendationRecord{memorydb, memorydb}, false},
		"azure cache never counted":     {[]config.RecommendationRecord{azure, azure}, false},
		"count zero skipped":            {[]config.RecommendationRecord{with(func(r *config.RecommendationRecord) { r.Count = 0 }), with(func(r *config.RecommendationRecord) { r.Count = 0 })}, false},
		"empty engine skipped":          {[]config.RecommendationRecord{ecRecWith(t, "", 2, "cache.r6g.large", ecRegion), ecRecWith(t, "", 2, "cache.r6g.large", ecRegion)}, false},
		"legacy slug vs canonical":      {[]config.RecommendationRecord{base(), canonical}, true},
		"no account vs scoped twin":     {[]config.RecommendationRecord{noAcct, withAccount(base(), "acct-X")}, true},
		"node type compared raw":        {[]config.RecommendationRecord{base(), with(func(r *config.RecommendationRecord) { r.ResourceType = "cache.R6G.large" })}, false},
		"different node type":           {[]config.RecommendationRecord{base(), with(func(r *config.RecommendationRecord) { r.ResourceType = "cache.r6g.xlarge" })}, false},
		"engine only on the rec column": {[]config.RecommendationRecord{base(), with(func(r *config.RecommendationRecord) { r.Details, r.Engine = nil, "redis" })}, true},
		"single rec":                    {[]config.RecommendationRecord{base()}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			exec := ecDupExec(tc.recs...)
			if name == "no account vs scoped twin" {
				acct := "acct-X"
				exec.CloudAccountID = &acct
			}
			assert.Equal(t, tc.refused, hasDuplicateElastiCachePool(exec))
		})
	}
}

func TestElastiCacheDuplicatePool_RefusedBeforeSubmission(t *testing.T) {
	exec := ecDupExec(ecCacheRec(t, "elasticache"), ecCacheRec(t, "elasticache"))
	run := runRedshiftFinalize(t, exec, nil, common.ServiceElastiCache, nil)

	require.Error(t, run.err)
	assert.Equal(t, elastiCacheDuplicatePoolRefusal, run.err.Error())
	assert.True(t, isBeforeSubmission(run.err))
	assert.True(t, exec.NoSubmission)
	assert.Equal(t, "failed", exec.Status)
	assert.Empty(t, run.opts, "no PurchaseCommitment call")
	require.NotEmpty(t, run.saves)
	stored := run.saves[len(run.saves)-1].Error
	assert.Equal(t, elastiCacheDuplicatePoolRefusal, stored)
	assert.Equal(t, stored, SanitizeStoredError(stored))
}

func TestElastiCacheDuplicatePool_PlanStepRootRefusedAndSuppressionsReleased(t *testing.T) {
	exec := ecDupExec(ecCacheRec(t, "elasticache"), ecCacheRec(t, "elasticache"))
	exec.PlanID, exec.StepNumber = "plan-x", 1
	run := runRedshiftFinalize(t, exec, nil, common.ServiceElastiCache, nil)

	require.Error(t, run.err)
	assert.Equal(t, elastiCacheDuplicatePoolRefusal, run.err.Error())
	assert.Empty(t, run.opts)
	store := run.store // the vacuous-assertion guard resolves only plain identifiers
	store.AssertCalled(t, "DeleteSuppressionsByExecutionTx")
}

// Fan-out children hold only their own account's recs, so same-pool recs of different accounts never collide.
func TestElastiCacheDuplicatePool_FanOutDifferentAccountsNotRefused(t *testing.T) {
	root := ecDupExec(withAccount(ecCacheRec(t, "elasticache"), "acct-A"), withAccount(ecCacheRec(t, "elasticache"), "acct-B"))
	root.PlanID, root.StepNumber = "plan-x", 1
	m := &Manager{config: new(MockConfigStore)}

	require.NoError(t, m.preExecutionRefusal(context.Background(), root))
	children, err := scopeExecutionsByAccount(root, []config.CloudAccount{{ID: "acct-A"}, {ID: "acct-B"}})
	require.NoError(t, err)
	require.Len(t, children, 2)
	for _, child := range children {
		assert.NoError(t, m.preExecutionRefusal(context.Background(), child))
	}
}

func TestElastiCacheDuplicatePool_RetryRefusedAgain(t *testing.T) {
	retry := ecDupExec(ecCacheRec(t, "elasticache"), ecCacheRec(t, "elasticache"))
	retry.RetryAttemptN = 1
	run := runRedshiftFinalize(t, retry, nil, common.ServiceElastiCache, nil)

	require.Error(t, run.err)
	assert.Equal(t, elastiCacheDuplicatePoolRefusal, run.err.Error())
	assert.Empty(t, run.opts)
}
