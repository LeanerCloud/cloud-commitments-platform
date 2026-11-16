package scheduler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type fakeAzureRefresher struct {
	err   error
	onRun func()
	calls int
}

func (f *fakeAzureRefresher) RefreshAzure(context.Context) error {
	f.calls++
	if f.onRun != nil {
		f.onRun()
	}
	return f.err
}

func collectWithAzureRefresher(t *testing.T, enabled []string, listErr error, r azureOptionsRefresher, onUpsert func()) *CollectResult {
	t.Helper()
	store := new(MockConfigStore)
	store.On("GetGlobalConfig", mock.Anything).Return(&config.GlobalConfig{EnabledProviders: enabled}, nil)
	store.On("UpsertRecommendations", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil).Run(func(mock.Arguments) {
		if onUpsert != nil {
			onUpsert()
		}
	}).Maybe()
	store.On("SetRecommendationsCollectionError", mock.Anything, mock.Anything).Return(nil).Maybe()
	store.On("ListCloudAccounts", mock.Anything, mock.Anything).Return(nil, listErr).Maybe()
	s := NewScheduler(SchedulerConfig{ConfigStore: store, EmailSender: new(MockEmailSender), AzureOptions: r})
	res, err := s.CollectRecommendations(context.Background(), "")
	require.NoError(t, err)
	return res
}

func TestCollectRecommendations_RefreshesAzureOptionsWhenAzureSucceeded(t *testing.T) {
	upserted := false
	r := &fakeAzureRefresher{onRun: func() {
		assert.True(t, upserted, "the refresh must run after persistCollection")
	}}
	res := collectWithAzureRefresher(t, []string{"azure"}, nil, r, func() { upserted = true })
	assert.Equal(t, []string{"azure"}, res.SuccessfulProviders)
	assert.Equal(t, 1, r.calls)
}

func TestCollectRecommendations_SkipsAzureOptionsWhenAzureAbsent(t *testing.T) {
	r := &fakeAzureRefresher{}
	collectWithAzureRefresher(t, []string{"gcp"}, nil, r, nil)
	assert.Zero(t, r.calls)
}

func TestCollectRecommendations_SkipsAzureOptionsWhenAzureFailed(t *testing.T) {
	t.Setenv("AZURE_SUBSCRIPTION_ID", "sub-a")
	r := &fakeAzureRefresher{}
	res := collectWithAzureRefresher(t, []string{"azure"}, errors.New("db down"), r, nil)
	require.Contains(t, res.FailedProviders, "azure")
	assert.Zero(t, r.calls)
}

func TestCollectRecommendations_AzureOptionsErrorDoesNotFailRun(t *testing.T) {
	r := &fakeAzureRefresher{err: errors.New("probe boom")}
	res := collectWithAzureRefresher(t, []string{"azure"}, nil, r, nil)
	assert.Equal(t, 1, r.calls)
	assert.Equal(t, []string{"azure"}, res.SuccessfulProviders)
	assert.Empty(t, res.FailedProviders)
}

func TestCollectRecommendations_NilAzureOptionsIsNoOp(t *testing.T) {
	res := collectWithAzureRefresher(t, []string{"azure"}, nil, nil, nil)
	assert.Equal(t, []string{"azure"}, res.SuccessfulProviders)
}

func TestRefreshAzureOptionsBestEffort_StopsBeforeRunDeadline(t *testing.T) {
	dl := time.Now().Add(time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), dl)
	defer cancel()
	var got time.Time
	r := &deadlineRefresher{got: &got}
	s := &Scheduler{azureOptions: r}
	s.refreshAzureOptionsBestEffort(ctx, []string{"azure"})
	assert.Equal(t, dl.Add(-azureOptionsDeadlineMargin), got)
}

type deadlineRefresher struct{ got *time.Time }

func (d *deadlineRefresher) RefreshAzure(ctx context.Context) error {
	*d.got, _ = ctx.Deadline()
	return nil
}

func TestCollectRecommendations_AzureOptionsRefreshRunsAfterNotification(t *testing.T) {
	ctx := context.Background()
	store := new(MockConfigStore)
	factory := new(MockProviderFactory)
	prov := new(MockProvider)
	recClient := new(MockRecommendationsClient)
	mailer := new(MockEmailSender)
	store.On("GetGlobalConfig", ctx).Return(&config.GlobalConfig{EnabledProviders: []string{"aws", "azure"}, DefaultTerm: 3, DefaultPayment: "all-upfront"}, nil)
	factory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(prov, nil)
	prov.On("GetRecommendationsClient", mock.Anything).Return(recClient, nil)
	recClient.On("GetAllRecommendations", mock.Anything).Return([]common.Recommendation{
		{Provider: common.ProviderAWS, Service: common.ServiceEC2, Region: "us-east-1", ResourceType: "m5.large", Count: 5, Term: "3yr", EstimatedSavings: 500},
	}, nil)
	mailed := false
	mailer.On("SendNewRecommendationsNotification", mock.Anything, mock.Anything).Return(nil).Run(func(mock.Arguments) { mailed = true })
	r := &fakeAzureRefresher{onRun: func() {
		assert.True(t, mailed, "the refresh must not delay the notification email")
	}}
	s := NewScheduler(SchedulerConfig{ConfigStore: store, EmailSender: mailer, ProviderFactory: factory, AzureOptions: r})
	res, err := s.CollectRecommendations(ctx, "")
	require.NoError(t, err)
	require.Equal(t, 1, res.Recommendations)
	assert.Equal(t, 1, r.calls)
}

const fakeSubscriptionID = "00000000-0000-0000-0000-000000000000"

func azureResponseError() *azcore.ResponseError {
	u := &url.URL{Scheme: "https", Host: "management.azure.com", Path: "/subscriptions/" + fakeSubscriptionID + "/providers/Microsoft.Capacity"}
	return &azcore.ResponseError{
		StatusCode: 403,
		ErrorCode:  "AuthorizationFailed",
		RawResponse: &http.Response{
			StatusCode: 403,
			Request:    &http.Request{Method: http.MethodGet, URL: u},
		},
	}
}

func refreshFailureLog(t *testing.T, refreshErr error) string {
	t.Helper()
	var out bytes.Buffer
	prevOut := logging.SetOutput(&out)
	prevLevel := logging.GetLevel()
	logging.SetLevelValue(logging.LevelDebug)
	t.Cleanup(func() {
		logging.SetOutput(prevOut)
		logging.SetLevelValue(prevLevel)
	})
	s := &Scheduler{azureOptions: &fakeAzureRefresher{err: refreshErr}}
	s.refreshAzureOptionsBestEffort(context.Background(), []string{"azure"})
	return out.String()
}

func TestRefreshAzureOptionsBestEffort_LogsResponseErrorStatusAndCodeWithoutURL(t *testing.T) {
	respErr := azureResponseError()
	require.Contains(t, respErr.Error(), fakeSubscriptionID, "fixture must carry the URL in Error()")
	got := refreshFailureLog(t, fmt.Errorf("probe: %w", respErr))
	assert.Contains(t, got, "403")
	assert.Contains(t, got, "AuthorizationFailed")
	assert.NotContains(t, got, fakeSubscriptionID)
	assert.NotContains(t, got, "management.azure.com")
	assert.Equal(t, 1, bytes.Count([]byte(got), []byte("refresh failed")))
}

func TestRefreshAzureOptionsBestEffort_LogsContextClass(t *testing.T) {
	got := refreshFailureLog(t, fmt.Errorf("probe: %w", context.DeadlineExceeded))
	assert.Contains(t, got, "deadline exceeded")
	got = refreshFailureLog(t, fmt.Errorf("probe: %w", context.Canceled))
	assert.Contains(t, got, "canceled")
	assert.NotContains(t, got, "deadline exceeded")
}

func TestRefreshAzureOptionsBestEffort_PlainErrorLogsTypeOnly(t *testing.T) {
	got := refreshFailureLog(t, fmt.Errorf("probe secret-detail: %w", errors.New("inner secret-detail")))
	assert.Contains(t, got, "*errors.errorString")
	assert.NotContains(t, got, "secret-detail")
}
