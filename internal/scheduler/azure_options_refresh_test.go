package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
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
