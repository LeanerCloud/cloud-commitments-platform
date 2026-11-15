package purchase

import (
	"errors"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ec2DueRow(id string, amount float64) config.LadderDueRow {
	r := dueRow(id, amount, true)
	r.Tranche.LayerType = ladder.LayerEC2InstanceSP
	return r
}

func TestSweep_EC2SPReadsStoredRecsBeforeTheClaimAndOneExecutionHoldsAllFamilies(t *testing.T) {
	f := newFireStore(fireCfg("c1"))
	f.rows["c1"] = []config.LadderDueRow{ec2DueRow("E1", 2.0), dueRow("T1", 1.0, true)}
	f.stored = []config.RecommendationRecord{storedEC2SPIn("us-east-1", "m5", 3), storedEC2SPIn("us-east-1", "c5", 1)}
	res, err := runFire(f)
	require.NoError(t, err)
	assert.Equal(t, LadderFireResult{Configs: 1, Due: 2, Claimed: 2, Announced: 2}, res)
	require.Len(t, f.storedFilters, 1)
	assert.Equal(t, config.RecommendationFilter{Provider: "aws", Service: "savings-plans-ec2instance", AccountIDs: []string{"acct-a"}}, f.storedFilters[0])
	assert.Equal(t, []string{"E1", "T1"}, f.filter.TrancheIDs)
	require.Len(t, f.saved, 2)
	require.Len(t, f.saved[0].Recommendations, 2, "one execution holds a rec per family")
	assert.Len(t, f.saved[1].Recommendations, 1)
}

func TestSweep_EC2SPWithoutARecIsUnresolvedAndTheGoodTrancheStillClaims(t *testing.T) {
	f := newFireStore(fireCfg("c1"))
	f.rows["c1"] = []config.LadderDueRow{ec2DueRow("E1", 2.0), dueRow("T1", 1.0, true)}
	res, err := runFire(f)
	require.NoError(t, err)
	assert.Equal(t, LadderFireResult{Configs: 1, Due: 2, Unresolved: 1, Claimed: 1, Announced: 1}, res)
	assert.Equal(t, []string{"T1"}, f.filter.TrancheIDs)
}

func TestSweep_ComputeOnlyConfigsDoNotReadStoredRecs(t *testing.T) {
	f := newFireStore(fireCfg("c1"))
	_, err := runFire(f)
	require.NoError(t, err)
	assert.Empty(t, f.storedFilters)
}

func TestSweep_StoredRecReadErrorClaimsNothing(t *testing.T) {
	f := newFireStore(fireCfg("c1"))
	f.rows["c1"] = []config.LadderDueRow{ec2DueRow("E1", 2.0)}
	f.storedErr = errors.New("db down")
	res, err := runFire(f)
	require.Error(t, err)
	assert.Equal(t, 1, res.Errored)
	assert.Zero(t, f.claimCalls)
}
