package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func azureSQLRec(engine string) config.RecommendationRecord {
	return config.RecommendationRecord{
		Provider: "azure", Service: "relational-db", Region: "westeurope",
		ResourceType: "GP_Gen5_2", Engine: engine, Count: 3, CloudAccountID: strPtr("acct-1"),
	}
}

func azureSQLSuppression(provider, service, engine string) config.PurchaseSuppression {
	return config.PurchaseSuppression{
		ExecutionID: "e1", AccountID: "acct-1", Provider: provider, Service: service,
		Region: "westeurope", ResourceType: "GP_Gen5_2", Engine: engine,
		SuppressedCount: 3, ExpiresAt: time.Now().Add(24 * time.Hour),
	}
}

// go#735 gave Advisor Azure SQL recs engine sqlserver (they had none). A
// grace-period suppression stored before the bump with engine "" must still
// hide the rec, or an auto-purchase plan could buy it again (issue #1950).
func TestApplySuppressions_AzureSQLEngineStableAcrossGoBump(t *testing.T) {
	for _, tc := range []struct {
		name      string
		supEngine string
		recEngine string
		hidden    bool
	}{
		{"old suppression hides new rec", "", "sqlserver", true},
		{"new suppression hides new rec", "sqlserver", "sqlserver", true},
		{"new suppression hides old rec", "sqlserver", "", true},
		{"old suppression hides old rec", "", "", true},
	} {
		store := &mockSuppressionStore{
			recs: []config.RecommendationRecord{azureSQLRec(tc.recEngine)},
			sups: []config.PurchaseSuppression{azureSQLSuppression("azure", "relational-db", tc.supEngine)},
		}
		got, err := (&Scheduler{config: store}).ListRecommendations(context.Background(), config.RecommendationFilter{})
		require.NoError(t, err, tc.name)
		assert.Empty(t, got, tc.name)
	}
}

func TestApplySuppressions_EngineMappingDoesNotWidenBeyondAzureSQL(t *testing.T) {
	aws := azureSQLRec("mysql")
	aws.Provider, aws.Service = "aws", "rds"
	for _, tc := range []struct {
		name string
		rec  config.RecommendationRecord
		sup  config.PurchaseSuppression
	}{
		{"aws rds engine empty does not hide mysql", aws, azureSQLSuppression("aws", "rds", "")},
		{"azure sql engine empty does not hide mysql", azureSQLRec("mysql"), azureSQLSuppression("azure", "relational-db", "")},
		{"azure sql sqlserver does not hide mysql", azureSQLRec("mysql"), azureSQLSuppression("azure", "relational-db", "sqlserver")},
	} {
		store := &mockSuppressionStore{recs: []config.RecommendationRecord{tc.rec}, sups: []config.PurchaseSuppression{tc.sup}}
		got, err := (&Scheduler{config: store}).ListRecommendations(context.Background(), config.RecommendationFilter{})
		require.NoError(t, err, tc.name)
		require.Len(t, got, 1, tc.name)
		assert.Zero(t, got[0].SuppressedCount, tc.name)
	}
}

// The SQL page path matches raw stored engine values, so the keys it sends
// must carry both spellings of the Azure SQL engine.
func TestSuppressionPageKeys_AzureSQLCarriesBothEngineSpellings(t *testing.T) {
	index := aggregateSuppressions([]config.PurchaseSuppression{azureSQLSuppression("azure", "relational-db", "")})
	keys := suppressionPageKeys(index)
	for _, engine := range []string{"", "sqlserver"} {
		assert.Contains(t, keys, config.SuppressionTupleKey("acct-1", "azure", "relational-db", "westeurope", "GP_Gen5_2", engine))
	}
}
