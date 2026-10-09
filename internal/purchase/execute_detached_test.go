package purchase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// detachFixture wires a direct single-account AWS purchase (issue #706).
type detachFixture struct {
	manager *Manager
	exec    *config.PurchaseExecution
	svc     *MockServiceClient
	saved   []string
	history int
	saveCtx context.Context
}

func newDetachFixture(t *testing.T) *detachFixture {
	t.Helper()
	f := &detachFixture{svc: new(MockServiceClient)}
	store := new(MockConfigStore)
	factory := new(MockProviderFactory)
	prov := new(MockProvider)
	stsMock := new(MockSTSClient)
	f.exec = &config.PurchaseExecution{
		ExecutionID: "exec-detach", StepNumber: 1,
		Recommendations: []config.RecommendationRecord{
			{Provider: "aws", Service: "ec2", ResourceType: "m5.large", Region: "us-east-1", Count: 1, UpfrontCost: 500, Selected: true},
		},
	}
	store.SavePurchaseExecutionFn = func(ctx context.Context, e *config.PurchaseExecution) error {
		f.saveCtx = ctx
		if err := ctx.Err(); err != nil {
			return err
		}
		f.saved = append(f.saved, e.Status)
		return nil
	}
	store.On("SavePurchaseHistory", mock.Anything, mock.Anything).Run(func(mock.Arguments) { f.history++ }).Return(nil)
	email := new(MockEmailSender)
	email.On("SendPurchaseConfirmation", mock.Anything, mock.Anything).Return(nil)
	stsMock.On("GetCallerIdentity", mock.Anything, mock.Anything).Return(&stsOut, nil)
	factory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(prov, nil)
	prov.On("GetServiceClient", mock.Anything, common.ServiceEC2, "us-east-1").Return(f.svc, nil)
	f.manager = &Manager{config: store, email: email, stsClient: stsMock, providerFactory: factory, dashboardURL: "https://d.example.com"}
	return f
}

var stsOut = sts.GetCallerIdentityOutput{Account: aws.String("123456789012")}

// Test A: the client disconnects while the provider call is in flight. The
// purchase committed, so exactly one call happened and the terminal row and
// history record must still be written. Pre-fix the save saw the cancelled
// request ctx and returned ErrAuditLoss.
func TestExecuteAndFinalize_ClientDisconnectMidPurchase_StillRecorded(t *testing.T) {
	f := newDetachFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.svc.On("PurchaseCommitment", mock.Anything, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { cancel() }).
		Return(common.PurchaseResult{Success: true, CommitmentID: "ri-1"}, nil).Once()

	err := f.manager.executeAndFinalize(ctx, f.exec)

	require.NoError(t, err)
	f.svc.AssertNumberOfCalls(t, "PurchaseCommitment", 1)
	assert.Equal(t, []string{"completed"}, f.saved)
	assert.Equal(t, 1, f.history)
}

// Test B: a caller ctx whose deadline already passed (30s request timeout)
// must not reach the provider or the save; both get their own bounded budget.
func TestExecuteAndFinalize_ExpiredCallerDeadline_UsesOwnBudgets(t *testing.T) {
	f := newDetachFixture(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	var provDeadline time.Time
	f.svc.On("PurchaseCommitment", mock.Anything, mock.Anything, mock.Anything).
		Run(func(a mock.Arguments) {
			c := a.Get(0).(context.Context)
			require.NoError(t, c.Err())
			provDeadline, _ = c.Deadline()
		}).
		Return(common.PurchaseResult{Success: true, CommitmentID: "ri-2"}, nil).Once()

	require.NoError(t, f.manager.executeAndFinalize(ctx, f.exec))

	// per-rec cap (30s) still bounds the provider call inside the 4m run budget.
	assert.WithinDuration(t, time.Now(), provDeadline, 30*time.Second)
	d, ok := f.saveCtx.Deadline()
	require.True(t, ok, "terminal save needs a deadline")
	assert.LessOrEqual(t, time.Until(d), terminalSaveTimeout)
	assert.Equal(t, []string{"completed"}, f.saved)
}

// Test C: a failed purchase with a cancelled caller still persists "failed".
func TestExecuteAndFinalize_ProviderErrorWithCancelledCaller_PersistsFailed(t *testing.T) {
	f := newDetachFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.svc.On("PurchaseCommitment", mock.Anything, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { cancel() }).
		Return(common.PurchaseResult{}, errors.New("provider rejected")).Once()

	err := f.manager.executeAndFinalize(ctx, f.exec)

	require.Error(t, err)
	assert.NotErrorIs(t, err, config.ErrAuditLoss)
	assert.Equal(t, []string{"failed"}, f.saved)
}
