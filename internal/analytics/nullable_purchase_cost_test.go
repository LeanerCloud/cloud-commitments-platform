package analytics

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectorUnknownUpfront(t *testing.T) {
	for _, costs := range [][]*float64{{nil}, {nil, new(float64(120))}, {new(float64(120)), nil}, {new(float64)}} {
		purchases := make([]config.PurchaseHistoryRecord, len(costs), len(costs)+1)
		unknown := false
		for i, cost := range costs {
			purchases[i] = activeRecord("aws", "ec2", "us-east-1", 1, 10, 0)
			purchases[i].UpfrontCost = cost
			unknown = unknown || cost == nil
		}
		other := activeRecord("aws", "ec2", "us-east-1", 1, 20, 240)
		other.AccountID = "other-account"
		purchases = append(purchases, other)
		groups, _, _, err := aggregatePurchases(context.Background(), purchases, time.Now())
		require.NoError(t, err)
		snapshots := buildSnapshots(groups, time.Now())
		require.Len(t, snapshots, 2)
		for _, snapshot := range snapshots {
			if snapshot.AccountID == other.AccountID {
				require.NotNil(t, snapshot.TotalCommitment)
				assert.Equal(t, 20.0, *snapshot.TotalCommitment)
				continue
			}
			assert.Equal(t, 10*float64(len(costs)), snapshot.TotalSavings)
			assert.Equal(t, len(costs), snapshot.Metadata["active_purchases"])
			body, err := json.Marshal(snapshot)
			require.NoError(t, err)
			if unknown {
				assert.Nil(t, snapshot.TotalCommitment)
				assert.Contains(t, string(body), `"total_commitment":null`)
			} else {
				require.NotNil(t, snapshot.TotalCommitment)
				assert.Zero(t, *snapshot.TotalCommitment)
				assert.Contains(t, string(body), `"total_commitment":0`)
			}
		}
	}
}
