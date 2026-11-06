//go:build integration

package server

import (
	"context"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/exchange"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
	ec2svc "github.com/LeanerCloud/cloud-commitments-go/providers/aws/services/ec2"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// [fail-first] #1050: an AUTO run through the real store refuses a candidate
// whose source RI another processing exchange holds (its outcome may be
// unknown at AWS), records why in result.Failed, and still runs the others.
func TestRIExchangeAuto_SourceInFlightRefusedRunContinues(t *testing.T) {
	setupCtx, setupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer setupCancel()
	conn := testhelpers.RequireMigratedDatabase(setupCtx, t, "../database/postgres/migrations").DB
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	store := config.NewPostgresStore(conn)
	const bigCap = "1000000.000000"
	shared, free := "ri-g1050-shared", "ri-g1050-free"

	blocker := &config.RIExchangeRecord{AccountID: fmt.Sprintf("%012d", 777777777777), Region: "us-east-1", SourceRIIDs: []string{shared},
		SourceInstanceType: "c5.xlarge", SourceCount: 1, TargetOfferingID: "offering-test", TargetInstanceType: "c6i.xlarge",
		TargetCount: 1, PaymentDue: "10.000000", Status: "processing", Mode: "auto"}
	// A small per-exchange cap keeps the blocker's ceiling from taking the run's daily headroom.
	_, err := store.ReserveRIExchange(ctx, blocker, bigCap, "20.000000")
	require.NoError(t, err)
	require.NotEmpty(t, blocker.ID)

	executed := map[string]int{}
	client := &mockExchangeClient{
		getQuoteFunc: func(context.Context, exchange.ExchangeQuoteRequest) (*exchange.ExchangeQuoteSummary, error) {
			return &exchange.ExchangeQuoteSummary{IsValidExchange: true, PaymentDueUSD: big.NewRat(100, 1), PaymentDueUSDStr: "100.000000", CurrencyCode: "USD"}, nil
		},
		executeFunc: func(_ context.Context, req exchange.ExchangeExecuteRequest) (string, *exchange.ExchangeQuoteSummary, error) {
			executed[req.ReservedIDs[0]]++
			return "accepted-" + req.ReservedIDs[0], &exchange.ExchangeQuoteSummary{IsValidExchange: true, PaymentDueUSD: big.NewRat(100, 1), PaymentDueUSDStr: "100.000000", CurrencyCode: "USD"}, nil
		},
	}
	ris := make([]ec2svc.ConvertibleRI, 0, 2)
	util := make([]recommendations.RIUtilization, 0, 2)
	for _, id := range []string{shared, free} {
		ris = append(ris, ec2svc.ConvertibleRI{ReservedInstanceID: id, InstanceType: "c5.xlarge", InstanceCount: 1, NormalizationFactor: 8,
			ProductDescription: "Linux/UNIX", InstanceTenancy: "default", Scope: "Region", Duration: 31536000, FixedPrice: 876})
		util = append(util, recommendations.RIUtilization{ReservedInstanceID: id, UtilizationPercent: 30})
	}
	riInfos, utilInfos, metadata := convertForAutoExchange(ris, util)
	result, err := exchange.RunAutoExchange(ctx, exchange.RunAutoExchangeParams{
		Store: newConfigExchangeStoreAdapter(store), ExchangeClient: client,
		LookupOffering: func(context.Context, exchange.LookupOfferingParams) (string, error) { return "offering-test", nil },
		RIs:            riInfos, Utilization: utilInfos, RIMetadata: metadata,
		Config:    exchange.RIExchangeConfig{Mode: "auto", UtilizationThreshold: 95, MaxPaymentPerExchangeUSD: 1000, MaxPaymentDailyUSD: 1000000},
		AccountID: fmt.Sprintf("%012d", 777777777778), Region: "us-east-1",
	})
	require.NoError(t, err)

	assert.Zero(t, executed[shared], "an in-flight source RI must never reach AWS again")
	require.Len(t, result.Failed, 1)
	assert.Equal(t, shared, result.Failed[0].SourceRIID)
	assert.Contains(t, result.Failed[0].Error, "already have an exchange in progress")
	assert.Contains(t, result.Failed[0].Error, shared)
	assert.NotContains(t, result.Failed[0].Error, blocker.ID, "the blocking exchange's id is never exposed (#1428)")
	assert.Equal(t, 1, executed[free], "the run continues past the refused candidate")
	require.Len(t, result.Completed, 1)
	assert.Equal(t, free, result.Completed[0].SourceRIID)

	row, err := store.GetRIExchangeRecord(ctx, blocker.ID)
	require.NoError(t, err)
	assert.Equal(t, "processing", row.Status, "the blocking exchange is left for the operator to resolve")
}
