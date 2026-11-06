package purchase

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const (
	lateExecTimeout = 300 * time.Millisecond // E
	lateMailDelay   = 100 * time.Millisecond
	lateAcctD       = "dddddddd-0000-0000-0000-000000000004"
)

type lateSave struct {
	exec     config.PurchaseExecution
	deadline time.Time
	done     time.Time
}

// lateLogBuf is a goroutine-safe log sink.
type lateLogBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lateLogBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lateLogBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// lateFixture drives executeAndFinalize over a four-account fan-out, one
// account at a time (or two): A buys at once, B is in flight when the run
// deadline passes, C and D never start (issue #1672).
type lateFixture struct {
	m     *Manager
	exec  *config.PurchaseExecution
	mu    sync.Mutex
	saves []lateSave
	mails []email.NotificationData
	logs  *lateLogBuf

	client *MockServiceClient
	// blockCMail makes the email about account C alone wait for its context
	// (at most 2s) and records when it ended in cMailEnd.
	blockCMail bool
	cMailEnd   time.Time
}

// newLateFixture builds the fan-out. acctSave handles the saves of started
// accounts (rows that are neither the root nor a skip row); nil saves at once.
func newLateFixture(t *testing.T, parallelism string, acctSave func(context.Context, *config.PurchaseExecution) error) *lateFixture {
	t.Helper()
	t.Setenv("CUDLY_MAX_ACCOUNT_PARALLELISM", parallelism)
	f := &lateFixture{logs: &lateLogBuf{}}
	prev := logging.SetOutput(f.logs)
	t.Cleanup(func() { logging.SetOutput(prev) })

	store := new(MockConfigStore)
	mailer := new(MockEmailSender)
	factory := new(MockProviderFactory)
	prov := new(MockProvider)
	client := new(MockServiceClient)
	f.client = client

	ids := []string{skipAcctA, skipAcctB, skipAcctC, lateAcctD}
	types := []string{"a.large", "b.large", "c.large", "d.large"}
	accounts := make([]config.CloudAccount, 0, len(ids))
	recs := make([]config.RecommendationRecord, 0, len(ids))
	for i, id := range ids {
		accounts = append(accounts, config.CloudAccount{ID: id, Name: id[:1], Provider: "aws", ExternalID: strings.Repeat(string(rune('1'+i)), 12), AWSAuthMode: "access_keys"})
		recs = append(recs, withAccount(config.RecommendationRecord{Provider: "aws", Service: "ec2", ResourceType: types[i], Region: "us-east-1",
			Count: 1, Savings: 75, UpfrontCost: 300, Selected: true}, id))
	}
	f.exec = &config.PurchaseExecution{ExecutionID: "exec-late", PlanID: "plan-late", Status: "running", IdempotencyKey: "lineage-late", Recommendations: recs}

	store.GetPlanAccountsFn = func(context.Context, string) ([]config.CloudAccount, error) { return accounts, nil }
	store.SavePurchaseExecutionFn = func(ctx context.Context, e *config.PurchaseExecution) error {
		s := lateSave{exec: *e}
		s.deadline, _ = ctx.Deadline()
		var err error
		if e.CloudAccountID != nil && !e.NoSubmission && acctSave != nil {
			err = acctSave(ctx, e)
		}
		s.done = time.Now()
		f.mu.Lock()
		defer f.mu.Unlock()
		f.saves = append(f.saves, s)
		return err
	}
	store.On("SavePurchaseHistory", mock.Anything, mock.Anything).Return(nil).Maybe()
	mailer.On("SendPurchaseConfirmation", mock.Anything, mock.Anything).Return(nil).Maybe()
	mailer.On("SendPurchaseFailedNotification", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			d := args.Get(1).(email.NotificationData)
			if f.blockCMail && len(d.Recommendations) == 1 && d.Recommendations[0].ResourceType == "c.large" {
				select {
				case <-time.After(2 * time.Second):
				case <-args.Get(0).(context.Context).Done():
				}
				f.mu.Lock()
				f.cMailEnd = time.Now()
				f.mu.Unlock()
			} else {
				time.Sleep(lateMailDelay)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			f.mails = append(f.mails, d)
		}).Return(nil).Maybe()
	factory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(prov, nil).Maybe()
	prov.On("GetServiceClient", mock.Anything, common.ServiceEC2, "us-east-1").Return(client, nil).Maybe()
	byType := func(rt string) interface{} {
		return mock.MatchedBy(func(r common.Recommendation) bool { return r.ResourceType == rt })
	}
	client.On("PurchaseCommitment", mock.Anything, byType("a.large"), mock.Anything).
		Return(common.PurchaseResult{Success: true, CommitmentID: "ri-a"}, nil).Maybe()
	// B's provider call hangs until the run context ends: it may have landed.
	client.On("PurchaseCommitment", mock.Anything, byType("b.large"), mock.Anything).
		Run(func(args mock.Arguments) { <-args.Get(0).(context.Context).Done() }).
		Return(common.PurchaseResult{}, context.DeadlineExceeded).Maybe()

	f.m = &Manager{config: store, email: mailer, providerFactory: factory,
		budgets: purchaseBudgets{execTimeout: lateExecTimeout, terminalSave: lateTerminalT, perRec: 5 * time.Second},
		credStore: &MockCredentialStore{LoadRawFn: func(context.Context, string, string) ([]byte, error) {
			return []byte(`{"access_key_id":"AKIAIOSFODNN7EXAMPLE","secret_access_key":"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}`), nil
		}}}
	return f
}

func (f *lateFixture) save(account string, skip bool) (lateSave, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.saves {
		isRoot := s.exec.CloudAccountID == nil
		switch {
		case account == "" && isRoot:
			return s, true
		case !isRoot && *s.exec.CloudAccountID == account && s.exec.NoSubmission == skip:
			return s, true
		}
	}
	return lateSave{}, false
}

// An account in flight when the run deadline passes saves its row on its
// slice, sends no email of its own, and joins the run's single email after the
// root save. Its provider call was cut off, so the row records an unknown
// outcome and the email says so, never "failed". The run ends by E+T plus the
// one email.
func TestExecuteAndFinalize_LateAccountBatchedIntoOneOutcomeUnknownEmail(t *testing.T) {
	f := newLateFixture(t, "1", func(context.Context, *config.PurchaseExecution) error {
		time.Sleep(30 * time.Millisecond) // a slow store, inside the T/2 slice
		return nil
	})
	start := time.Now()
	err := f.m.executeAndFinalize(context.Background(), f.exec)
	elapsed := time.Since(start)

	var partial *multiAccountPartialError
	require.ErrorAs(t, err, &partial, "A committed, so the run is an acked partial")
	assert.Equal(t, "partially_completed", f.exec.Status)

	root, ok := f.save("", false)
	require.True(t, ok, "the root must be saved")
	assert.Equal(t, "partially_completed", root.exec.Status, "the root is saved WITH its terminal status")
	assert.WithinDuration(t, start.Add(lateExecTimeout+lateTerminalT), root.deadline, 80*time.Millisecond, "root deadline is E+T")

	b, ok := f.save(skipAcctB, false)
	require.True(t, ok, "the late account saves its own row")
	assert.Equal(t, "failed", b.exec.Status)
	assert.False(t, b.exec.NoSubmission, "nothing proves B submitted nothing")
	assert.Contains(t, b.exec.Error, common.ErrOutcomeUnknown.Error(), "B's call was cut off: outcome unknown, not a retryable failure")
	assert.WithinDuration(t, start.Add(lateExecTimeout+lateTerminalT/2), b.deadline, 80*time.Millisecond, "account slice is E+T/2")
	assert.True(t, b.done.Before(root.done), "the root saves after the late account it summarizes")

	for _, id := range []string{skipAcctC, lateAcctD} {
		s, ok := f.save(id, true)
		require.True(t, ok, "skip row for %s", id)
		assert.True(t, s.done.After(root.done), "skip rows follow the root")
		assert.WithinDuration(t, start.Add(lateExecTimeout+lateTerminalT), s.deadline, 80*time.Millisecond)
	}

	require.Len(t, f.mails, 1, "one email for the late account and the skipped ones, none per account")
	assert.True(t, f.mails[0].OutcomeUnknown, "the late account was cut off mid-call")
	assert.Len(t, f.mails[0].Recommendations, 3, "B, C and D")
	assert.Less(t, elapsed, lateExecTimeout+lateTerminalT+lateMailDelay+150*time.Millisecond, "inside E+T+N")
}

// Every account save outruns its T/2 slice. Each lost row is logged as AUDIT
// LOSS with its IDs, the root still saves with its status inside its own
// slice, and the skip rows and the email still follow.
func TestExecuteAndFinalize_AccountSavesStarveNotTheRoot(t *testing.T) {
	var mu sync.Mutex
	ended := map[string]time.Time{}
	f := newLateFixture(t, "2", func(ctx context.Context, e *config.PurchaseExecution) error {
		<-ctx.Done() // never finishes inside its slice
		mu.Lock()
		ended[*e.CloudAccountID] = time.Now()
		mu.Unlock()
		return ctx.Err()
	})
	start := time.Now()
	err := f.m.executeAndFinalize(context.Background(), f.exec)
	elapsed := time.Since(start)

	var partial *multiAccountPartialError
	require.ErrorAs(t, err, &partial)
	root, ok := f.save("", false)
	require.True(t, ok, "the root is saved although every account save timed out")
	assert.Equal(t, "partially_completed", root.exec.Status)

	slice := start.Add(lateExecTimeout + lateTerminalT/2)
	logs := f.logs.String()
	mu.Lock()
	defer mu.Unlock()
	for _, id := range []string{skipAcctA, skipAcctB} {
		require.Contains(t, ended, id, "account %s save started", id)
		assert.WithinDuration(t, slice, ended[id], 80*time.Millisecond, "%s save is cut at E+T/2", id)
		row, _ := f.save(id, false)
		assert.Contains(t, logs, "AUDIT LOSS: failed to save the record of execution "+row.exec.ExecutionID+" for account "+id)
	}
	for _, id := range []string{skipAcctC, lateAcctD} {
		_, ok := f.save(id, true)
		assert.True(t, ok, "skip row for %s still saved after the root", id)
	}
	assert.Len(t, f.mails, 1)
	assert.Less(t, elapsed, lateExecTimeout+lateTerminalT+lateMailDelay+150*time.Millisecond)
}

// A cut-off call is outcome unknown only when the run context ended: a
// provider error on a live context stays a plain failure.
func TestExecuteSinglePurchase_OnlyACutOffCallIsOutcomeUnknown(t *testing.T) {
	for _, cut := range []bool{false, true} {
		store := new(MockConfigStore)
		factory := new(MockProviderFactory)
		prov := new(MockProvider)
		client := new(MockServiceClient)
		ctx, cancel := context.WithCancel(context.Background())
		client.On("PurchaseCommitment", mock.Anything, mock.Anything, mock.Anything).
			Run(func(mock.Arguments) {
				if cut {
					cancel()
				}
			}).Return(common.PurchaseResult{}, errors.New("connection reset"))
		factory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(prov, nil)
		prov.On("GetServiceClient", mock.Anything, common.ServiceEC2, "us-east-1").Return(client, nil)
		m := &Manager{config: store, providerFactory: factory}
		_, err := m.executeSinglePurchase(ctx, config.RecommendationRecord{Provider: "aws", Service: "ec2", ResourceType: "m5.large", Region: "us-east-1", Count: 1},
			nil, common.PurchaseOptions{})
		require.Error(t, err)
		assert.Equal(t, cut, errors.Is(err, common.ErrOutcomeUnknown), "cut=%v: %v", cut, err)
		cancel()
	}
}

// The cut-off text must read in History exactly as the plain failure it
// replaced, and must never count as a proven refusal that unlocks a retry.
func TestCutOffCallText_HistoryAndRefusalUnchanged(t *testing.T) {
	const old = "purchase failed: context deadline exceeded"
	cut := "purchase failed: " + common.ErrOutcomeUnknown.Error() + ": context deadline exceeded"
	assert.Equal(t, SanitizeStoredError(old), SanitizeStoredError(cut))
	recs := []config.RecommendationRecord{{Provider: "aws", Service: "ec2"}}
	assert.False(t, IsDefiniteAWSRefusal(recs, "ec2 m5.large: "+cut))
}

// An account that fails just before the run deadline sends its own email, and a
// slow mailer must not keep the fan-out busy past the account slice: the email
// is cut at E+T/2 and the root still saves with its status inside its own slice.
func TestExecuteAndFinalize_AccountFailureEmailSharesTheAccountSlice(t *testing.T) {
	f := newLateFixture(t, "2", nil)
	f.blockCMail = true
	start := time.Now()
	failAt := start.Add(lateExecTimeout - 30*time.Millisecond)
	f.client.On("PurchaseCommitment", mock.Anything, mock.MatchedBy(func(r common.Recommendation) bool { return r.ResourceType == "c.large" }), mock.Anything).
		Run(func(mock.Arguments) { time.Sleep(time.Until(failAt)) }).
		Return(common.PurchaseResult{}, errors.New("insufficient capacity")).Maybe()

	err := f.m.executeAndFinalize(context.Background(), f.exec)
	elapsed := time.Since(start)

	var partial *multiAccountPartialError
	require.ErrorAs(t, err, &partial)
	root, ok := f.save("", false)
	require.True(t, ok, "the root is saved")
	assert.Equal(t, "partially_completed", root.exec.Status)
	f.mu.Lock()
	defer f.mu.Unlock()
	require.False(t, f.cMailEnd.IsZero(), "C failed before the deadline and emailed on its own")
	assert.WithinDuration(t, start.Add(lateExecTimeout+lateTerminalT/2), f.cMailEnd, 80*time.Millisecond, "C's email is cut at E+T/2")
	assert.Less(t, elapsed, lateExecTimeout+lateTerminalT+lateMailDelay+150*time.Millisecond, "inside E+T+N")
}
