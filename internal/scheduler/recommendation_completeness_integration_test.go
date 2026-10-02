//go:build integration

package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	awsprovider "github.com/LeanerCloud/cloud-commitments-go/providers/aws"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type completenessHTTP struct {
	resourceType string
	details      []string
	fallback     []string
	failScope    bool
	rdsCalls     atomic.Int32
	unexpected   atomic.Int32
	mu           sync.Mutex
	requests     []completenessRequest
}

type completenessRequest struct {
	Operation            string
	Service              string
	LookbackPeriodInDays string
	TermInYears          string
	PaymentOption        string
	SavingsPlansType     string
}

func (f *completenessHTTP) Do(req *http.Request) (*http.Response, error) {
	var input completenessRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		return nil, err
	}
	input.Operation = req.Header.Get("X-Amz-Target")
	f.mu.Lock()
	f.requests = append(f.requests, input)
	f.mu.Unlock()
	body := any(map[string]any{})
	status := http.StatusOK
	switch req.Header.Get("X-Amz-Target") {
	case "AWSInsightsIndexService.GetReservationPurchaseRecommendation":
		allowed := map[string]bool{
			"Amazon Elastic Compute Cloud - Compute": true, "Amazon Relational Database Service": true,
			"Amazon ElastiCache": true, "Amazon OpenSearch Service": true, "Amazon Redshift": true,
		}
		if !allowed[input.Service] {
			f.unexpected.Add(1)
			return nil, fmt.Errorf("invalid Cost Explorer Service %q", input.Service)
		}
		details := []any{}
		if input.Service == "Amazon Relational Database Service" {
			call := f.rdsCalls.Add(1)
			quantities := f.details
			if input.LookbackPeriodInDays == "THIRTY_DAYS" {
				quantities = f.fallback
			}
			for _, quantity := range quantities {
				details = append(details, map[string]any{
					"RecommendedNumberOfInstancesToPurchase": quantity,
					"EstimatedMonthlySavingsAmount":          "10", "EstimatedMonthlyOnDemandCost": "30",
					"InstanceDetails": map[string]any{"RDSInstanceDetails": map[string]any{
						"InstanceType": f.resourceType, "Region": "us-east-1", "DeploymentOption": "Single-AZ",
					}},
				})
			}
			if f.failScope && call == 1 {
				status = http.StatusBadRequest
			}
		}
		body = map[string]any{"Recommendations": []any{map[string]any{"RecommendationDetails": details}}}
	case "AWSInsightsIndexService.GetSavingsPlansPurchaseRecommendation", "AWSInsightsIndexService.GetReservationCoverage":
	default:
		f.unexpected.Add(1)
		return nil, fmt.Errorf("unexpected AWS operation %s", req.Header.Get("X-Amz-Target"))
	}
	if status != http.StatusOK {
		body = map[string]any{"__type": "AccessDeniedException", "message": "synthetic scope failure"}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/x-amz-json-1.1"}},
		Body: io.NopCloser(strings.NewReader(string(raw))), Request: req}, nil
}

func completenessProvider(t *testing.T, fixture *completenessHTTP) *MockProvider {
	t.Helper()
	client := awsprovider.NewRecommendationsClient(aws.Config{
		Region: "us-east-1", HTTPClient: fixture, Retryer: func() aws.Retryer { return aws.NopRetryer{} },
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "synthetic", SecretAccessKey: "synthetic"}, nil
		}),
	})
	prov := new(MockProvider)
	prov.On("GetRecommendationsClient", mock.Anything).Return(client, nil).Once()
	t.Cleanup(func() {
		prov.AssertExpectations(t)
		require.Zero(t, fixture.unexpected.Load())
		require.Positive(t, fixture.rdsCalls.Load())
	})
	return prov
}

func TestAWSRecommendationCompletenessPersistence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		registered bool
		details    []string
		fallback   []string
		failScope  bool
		complete   bool
		wantRows   int
	}{
		{name: "ambient mixed", details: []string{"2", "invalid"}, wantRows: 6},
		{name: "ambient all invalid", details: []string{"invalid"}},
		{name: "ambient valid", details: []string{"2"}, complete: true, wantRows: 6},
		{name: "ambient empty", complete: true},
		{name: "configured lookback returns valid fallback", fallback: []string{"2"}, complete: true, wantRows: 1},
		{name: "configured lookback remains empty", fallback: []string{}, complete: true},
		{name: "ambient all invalid then clean fallback", details: []string{"invalid"}, fallback: []string{"2"}, wantRows: 1},
		{name: "ambient empty then invalid fallback", fallback: []string{"invalid"}},
		{name: "ambient failed API scope", details: []string{"2"}, failScope: true, wantRows: 5},
		{name: "registered mixed", registered: true, details: []string{"2", "invalid"}, wantRows: 6},
		{name: "registered all invalid", registered: true, details: []string{"invalid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			container, err := testhelpers.SetupPostgresContainer(ctx, t)
			require.NoError(t, err)
			t.Cleanup(func() { container.Cleanup(ctx) })
			migrationPath, err := filepath.Abs("../database/postgres/migrations")
			require.NoError(t, err)
			require.NoError(t, migrations.RunMigrations(ctx, container.DB.Pool(), migrationPath, "", ""))
			store := config.NewPostgresStore(container.DB)
			factory := new(MockProviderFactory)
			fixture := &completenessHTTP{resourceType: "db.t3.medium", details: tc.details, fallback: tc.fallback, failScope: tc.failScope}
			factory.On("CreateAndValidateProvider", mock.Anything, "aws", (*provider.ProviderConfig)(nil)).
				Return(completenessProvider(t, fixture), nil).Once()
			var accountID *string
			var completeAccountID string
			if tc.registered {
				id := uuid.NewString()
				accountID = &id
				completeAccountID = uuid.NewString()
				for _, acct := range []config.CloudAccount{
					{ID: id, Name: "incomplete", Provider: "aws", ExternalID: "111111111111", Enabled: true, AWSAuthMode: "role_arn"},
					{ID: completeAccountID, Name: "complete", Provider: "aws", ExternalID: "222222222222", Enabled: true, AWSAuthMode: "role_arn", AWSRoleARN: "arn:aws:iam::222222222222:role/synthetic"},
				} {
					require.NoError(t, store.CreateCloudAccount(ctx, &acct))
				}
				factory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.MatchedBy(func(cfg *provider.ProviderConfig) bool { return cfg != nil })).
					Return(completenessProvider(t, &completenessHTTP{resourceType: "db.r5.large", details: []string{"2"}}), nil).Once()
			}
			seed := []config.RecommendationRecord{
				{ID: "missing-offer", Provider: "aws", CloudAccountID: accountID, Service: "rds", Region: "us-east-1", ResourceType: "db.t3.large", Savings: 40, Count: 1, Term: 12, Payment: "no-upfront"},
				{ID: "unswept-provider", Provider: "azure", Service: "vm", Region: "eastus", ResourceType: "D2", Savings: 20, Count: 1, Term: 12, Payment: "no-upfront"},
			}
			if tc.registered {
				seed = append(seed, config.RecommendationRecord{ID: "complete-stale", Provider: "aws", CloudAccountID: &completeAccountID,
					Service: "rds", Region: "us-east-1", ResourceType: "db.t3.large", Savings: 40, Count: 1, Term: 12, Payment: "no-upfront"})
			}
			require.NoError(t, store.UpsertRecommendations(ctx, time.Now().Add(-time.Hour), seed, nil))
			s := &Scheduler{config: store, providerFactory: factory}
			var globalCfg *config.GlobalConfig
			if tc.fallback != nil {
				globalCfg = &config.GlobalConfig{DefaultTerm: 1, DefaultPayment: "no-upfront", RecommendationsLookbackDays: 30}
			}
			recs, ids, err := s.collectAWSRecommendations(ctx, globalCfg)
			require.NoError(t, err)
			wantRows := tc.wantRows
			switch {
			case tc.registered:
				wantRows += 6
				require.Equal(t, []string{completeAccountID}, ids)
			case tc.complete:
				require.Equal(t, []string{""}, ids)
			default:
				require.Empty(t, ids)
			}
			require.Len(t, recs, wantRows)
			s.persistCollection(ctx, recs, expandSuccessfulCollects("aws", ids), nil)
			rows, err := store.ListStoredRecommendations(ctx, config.RecommendationFilter{})
			require.NoError(t, err)
			byID := make(map[string]config.RecommendationRecord, len(rows))
			for _, row := range rows {
				byID[row.ID] = row
			}
			_, missing := byID["missing-offer"]
			require.Equal(t, !tc.complete, missing, "incomplete collection must retain previous offers")
			require.Contains(t, byID, "unswept-provider")
			require.NotContains(t, byID, "complete-stale")
			for _, rec := range recs {
				require.Contains(t, byID, rec.ID, "surviving SDK recommendations must be persisted")
				require.Equal(t, rec.CloudAccountID, byID[rec.ID].CloudAccountID)
			}
			fallbackServices := []string{}
			for _, request := range fixture.requests {
				if request.Operation == "AWSInsightsIndexService.GetReservationCoverage" {
					continue
				}
				require.Contains(t, []string{"SEVEN_DAYS", "THIRTY_DAYS"}, request.LookbackPeriodInDays)
				if request.LookbackPeriodInDays == "SEVEN_DAYS" {
					continue
				}
				require.Equal(t, "ONE_YEAR", request.TermInYears)
				require.Equal(t, "NO_UPFRONT", request.PaymentOption)
				service := request.Service
				if request.Operation == "AWSInsightsIndexService.GetSavingsPlansPurchaseRecommendation" {
					service = "SP:" + request.SavingsPlansType
				}
				fallbackServices = append(fallbackServices, service)
			}
			if tc.fallback != nil {
				require.Equal(t, int32(7), fixture.rdsCalls.Load())
				require.ElementsMatch(t, []string{
					"Amazon Elastic Compute Cloud - Compute", "Amazon Relational Database Service", "Amazon ElastiCache",
					"Amazon OpenSearch Service", "Amazon Redshift", "SP:COMPUTE_SP", "SP:EC2_INSTANCE_SP", "SP:SAGEMAKER_SP", "SP:DATABASE_SP",
				}, fallbackServices)
			} else {
				require.Empty(t, fallbackServices)
			}
			factory.AssertExpectations(t)
		})
	}
}
