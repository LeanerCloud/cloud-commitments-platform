package purchase

// #682 P1: a GCP compute rec is refused before PurchaseCommitment when the
// project already holds a same-region, same-family commitment that started in
// the last 24 hours, and fails closed when listing errors. The real library GCP
// client runs against a fake commitments service, so the dedupe, the filter and
// the Insert name are the pinned library's own.

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/compute/apiv1/computepb"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/services/computeengine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/proto"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

const (
	cudProject = "proj"
	cudRegion  = "us-central1"
)

var (
	familyN2 = computepb.Commitment_GENERAL_PURPOSE_N2.String()
	familyE2 = computepb.Commitment_GENERAL_PURPOSE_E2.String()
)

type cudIter struct {
	items []*computepb.Commitment
	err   error
}

func (it *cudIter) Next() (*computepb.Commitment, error) {
	if it.err != nil {
		return nil, it.err
	}
	if len(it.items) == 0 {
		return nil, iterator.Done
	}
	next := it.items[0]
	it.items = it.items[1:]
	return next, nil
}

// fakeCUDService stores commitments like the API: Insert adds a live commitment
// and answers 409 for a name that already exists.
type fakeCUDService struct {
	mu       sync.Mutex
	items    []*computepb.Commitment
	listErr  error
	inserted []string
}

func (f *fakeCUDService) List(context.Context, *computepb.ListRegionCommitmentsRequest) computeengine.CommitmentsIterator {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &cudIter{items: append([]*computepb.Commitment(nil), f.items...), err: f.listErr}
}

func (f *fakeCUDService) find(name string) *computepb.Commitment {
	for _, c := range f.items {
		if c.GetName() == name {
			return c
		}
	}
	return nil
}

func (f *fakeCUDService) Insert(_ context.Context, req *computepb.InsertRegionCommitmentRequest) (computeengine.CommitmentsOperation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res := req.GetCommitmentResource()
	f.inserted = append(f.inserted, res.GetName())
	if f.find(res.GetName()) != nil {
		return nil, &googleapi.Error{Code: http.StatusConflict}
	}
	stored := proto.Clone(res).(*computepb.Commitment)
	stored.Status = proto.String(computepb.Commitment_ACTIVE.String())
	stored.StartTimestamp = proto.String(time.Now().UTC().Format(time.RFC3339))
	f.items = append(f.items, stored)
	return fakeCommitmentOp{}, nil
}

func (f *fakeCUDService) Get(_ context.Context, req *computepb.GetRegionCommitmentRequest) (*computepb.Commitment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.find(req.GetCommitment()); c != nil {
		return c, nil
	}
	return nil, errors.New("not found")
}

func (f *fakeCUDService) Close() error { return nil }

func (f *fakeCUDService) insertCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.inserted)
}

// cudFixture is a 4 vCPU / 16 GB, 12-month commitment, matching what a
// cudRec (term 1) asks Insert for.
func cudFixture(name, family string, age time.Duration) *computepb.Commitment {
	return &computepb.Commitment{
		Name:           proto.String(name),
		Plan:           proto.String(computepb.Commitment_TWELVE_MONTH.String()),
		Type:           proto.String(family),
		Status:         proto.String(computepb.Commitment_ACTIVE.String()),
		StartTimestamp: proto.String(time.Now().Add(-age).UTC().Format(time.RFC3339)),
		Resources: []*computepb.ResourceCommitment{
			{Type: proto.String(computepb.ResourceCommitment_VCPU.String()), Amount: proto.Int64(4)},
			{Type: proto.String(computepb.ResourceCommitment_MEMORY.String()), Amount: proto.Int64(16384)},
		},
	}
}

func cudRec(t *testing.T, family, region string) config.RecommendationRecord {
	t.Helper()
	blob, err := common.MarshalServiceDetails(&common.ComputeDetails{GCPCommitmentType: family, MemoryGB: 16})
	require.NoError(t, err)
	return config.RecommendationRecord{
		Provider: "gcp", Service: "compute", Region: region, ResourceType: "n2-standard-4",
		Count: 4, Term: 1, Payment: "monthly", Selected: true, Details: blob,
	}
}

const cudLineage = "lineage-682"

func cudOwnName(index int) string {
	return gcpCommitmentName(common.DeriveIdempotencyToken(cudLineage, index))
}

// gcpRun drives processPurchaseRecommendations for the recs against svc.
func gcpRun(t *testing.T, svc *fakeCUDService, lineage string, recs ...config.RecommendationRecord) ([]string, bool, []config.RecommendationRecord) {
	t.Helper()
	client, err := computeengine.NewClient(cudProject, cudRegion)
	require.NoError(t, err)
	client.SetCommitmentsService(svc)

	store := new(MockConfigStore)
	factory, prov := new(MockProviderFactory), new(MockProvider)
	store.On("SavePurchaseHistory", mock.Anything, mock.Anything).Return(nil).Maybe()
	factory.On("CreateAndValidateProvider", mock.Anything, "gcp", mock.Anything).Return(prov, nil)
	prov.On("GetServiceClient", mock.Anything, common.ServiceCompute, mock.Anything).Return(client, nil)
	m := &Manager{config: store, providerFactory: factory}

	exec := &config.PurchaseExecution{ExecutionID: "e", PlanID: "p", IdempotencyKey: lineage, Recommendations: recs}
	_, _, errs, allRefused, procErr := m.processPurchaseRecommendations(context.Background(), exec, &config.PurchasePlan{Name: "P"}, cudProject, nil, "")
	require.NoError(t, procErr)
	return errs, allRefused, exec.Recommendations
}

func TestGCPRecent_RefusedBeforePurchase(t *testing.T) {
	cases := map[string]struct {
		svc  *fakeCUDService
		note string
	}{
		"recent same-family commitment": {
			&fakeCUDService{items: []*computepb.Commitment{cudFixture("someone-else", familyN2, time.Hour)}},
			gcpRecentCommitmentNote,
		},
		"listing errors": {
			&fakeCUDService{listErr: errors.New("malformed commitment")},
			gcpDuplicateCheckFailedNote,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			errs, allRefused, recs := gcpRun(t, tc.svc, cudLineage, cudRec(t, familyN2, cudRegion))
			require.Len(t, errs, 1)
			assert.Equal(t, tc.note, recs[0].Error, "the stored rec error is exactly the fixed note")
			assert.Equal(t, tc.note, SanitizeStoredError(recs[0].Error))
			assert.Zero(t, tc.svc.insertCount(), "no Insert reached the provider")
			assert.True(t, allRefused, "every rec refused before submission feeds no_submission")
			assert.False(t, recs[0].Purchased)
		})
	}
}

func TestGCPRecent_Purchased(t *testing.T) {
	cases := map[string]struct {
		items  []*computepb.Commitment
		region string
	}{
		"recent commitment in another family": {[]*computepb.Commitment{cudFixture("other", familyE2, time.Hour)}, cudRegion},
		"recent commitment in another region": {[]*computepb.Commitment{cudFixture("other", familyN2, time.Hour)}, "europe-west1"},
		"commitment older than 24h":           {[]*computepb.Commitment{cudFixture("old", familyN2, 25*time.Hour)}, cudRegion},
		"own name after a lost response":      {[]*computepb.Commitment{cudFixture(cudOwnName(0), familyN2, time.Minute)}, cudRegion},
		"no commitments, project unknown":     {nil, cudRegion},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc := &fakeCUDService{items: tc.items}
			errs, allRefused, recs := gcpRun(t, svc, cudLineage, cudRec(t, familyN2, tc.region))
			require.Empty(t, errs)
			assert.False(t, allRefused)
			assert.True(t, recs[0].Purchased)
			assert.Equal(t, 1, svc.insertCount(), "the purchase call ran")
		})
	}
}

func TestGCPRecent_OwnNameRetryAdoptsExistingCommitment(t *testing.T) {
	svc := &fakeCUDService{items: []*computepb.Commitment{cudFixture(cudOwnName(0), familyN2, time.Minute)}}
	errs, _, recs := gcpRun(t, svc, cudLineage, cudRec(t, familyN2, cudRegion))
	require.Empty(t, errs)
	assert.Equal(t, []string{cudOwnName(0)}, svc.inserted, "Insert hit the 409 and the library adopted the commitment")
	assert.Equal(t, cudOwnName(0), recs[0].PurchaseID)
}

// End to end through the real go client: the retry's own CUD and another
// lineage's same-family CUD are both recent; the retry still adopts its own.
func TestGCPRecent_OwnNameRetryAdoptsDespiteForeignRecentCUD(t *testing.T) {
	foreign := gcpCommitmentName(common.DeriveIdempotencyToken("other-lineage", 0))
	svc := &fakeCUDService{items: []*computepb.Commitment{
		cudFixture(cudOwnName(0), familyN2, time.Minute),
		cudFixture(foreign, familyN2, time.Minute),
	}}
	errs, allRefused, recs := gcpRun(t, svc, cudLineage, cudRec(t, familyN2, cudRegion))
	require.Empty(t, errs)
	assert.False(t, allRefused)
	assert.Equal(t, cudOwnName(0), recs[0].PurchaseID, "the retry adopted its own commitment")
}

func TestGCPRecent_TwoSameFamilyRecsInOneExecutionBothBought(t *testing.T) {
	svc := &fakeCUDService{}
	errs, allRefused, recs := gcpRun(t, svc, cudLineage, cudRec(t, familyN2, cudRegion), cudRec(t, familyN2, cudRegion))
	require.Empty(t, errs, "an earlier rec of the same run is not a duplicate of a later one")
	assert.False(t, allRefused)
	assert.True(t, recs[0].Purchased && recs[1].Purchased)
	assert.ElementsMatch(t, []string{cudOwnName(0), cudOwnName(1)}, svc.inserted)
}

func TestGCPRecent_FanOutSiblingsOwnTheirOwnNames(t *testing.T) {
	root := "root-lineage"
	recs := []config.RecommendationRecord{cudRec(t, familyN2, cudRegion)}
	a := gcpOwnCommitmentNames(&config.PurchaseExecution{IdempotencyKey: root + ":acct-a", Recommendations: recs}, []int{0})
	b := gcpOwnCommitmentNames(&config.PurchaseExecution{IdempotencyKey: root + ":acct-b", Recommendations: recs}, []int{0})
	require.Len(t, a, 1)
	require.Len(t, b, 1)
	for name := range a {
		assert.False(t, b[name], "siblings derive different names")
	}
	for _, acct := range []string{"acct-a", "acct-b"} {
		svc := &fakeCUDService{} // each cloud account is its own project
		errs, _, got := gcpRun(t, svc, root+":"+acct, recs...)
		require.Empty(t, errs)
		assert.True(t, got[0].Purchased)
	}
}

func TestGCPRecent_RefusedPlusBoughtIsNotAllRefused(t *testing.T) {
	svc := &fakeCUDService{items: []*computepb.Commitment{cudFixture("someone-else", familyN2, time.Hour)}}
	errs, allRefused, recs := gcpRun(t, svc, cudLineage, cudRec(t, familyN2, cudRegion), cudRec(t, familyE2, cudRegion))
	require.Len(t, errs, 1)
	assert.False(t, allRefused)
	assert.False(t, recs[0].Purchased)
	assert.True(t, recs[1].Purchased)
	assert.Equal(t, []string{cudOwnName(1)}, svc.inserted)
}

func TestGCPOwnCommitmentNames_OnlySelectedGCPComputeRecs(t *testing.T) {
	gcp := cudRec(t, familyN2, cudRegion)
	unselected := gcp
	unselected.Selected = false
	aws := config.RecommendationRecord{Provider: "aws", Service: "ec2", Selected: true}
	otherGCP := config.RecommendationRecord{Provider: "gcp", Service: "relational-db", Selected: true}
	exec := &config.PurchaseExecution{IdempotencyKey: cudLineage, Recommendations: []config.RecommendationRecord{gcp, unselected, aws, otherGCP}}
	assert.Equal(t, map[string]bool{cudOwnName(0): true}, gcpOwnCommitmentNames(exec, selectedIndices(exec.Recommendations)))
}

// The mirror must equal the name the pinned library gives Insert.
func TestGCPCommitmentName_MatchesLibraryInsertName(t *testing.T) {
	svc := &fakeCUDService{}
	client, err := computeengine.NewClient(cudProject, cudRegion)
	require.NoError(t, err)
	client.SetCommitmentsService(svc)
	token := common.DeriveIdempotencyToken(cudLineage, 3)
	rec := common.Recommendation{ResourceType: "n2-standard-4", Term: "1yr", Count: 4, Details: common.ComputeDetails{MemoryGB: 16}}

	_, err = client.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{IdempotencyToken: token})
	require.NoError(t, err)
	require.Len(t, svc.inserted, 1)
	assert.Equal(t, svc.inserted[0], gcpCommitmentName(token))
}

// filterSpy is a client that implements the GCP filter and records its input.
type filterSpy struct {
	*MockServiceClient
	calls    int
	gotRecs  []common.Recommendation
	filtered bool
}

func (s *filterSpy) FilterRecommendationsForRecentCommitments(recs []common.Recommendation, _ []common.Commitment) (passed, filtered []common.Recommendation, err error) {
	s.calls++
	s.gotRecs = recs
	if s.filtered {
		return nil, recs, nil
	}
	return recs, nil, nil
}

func spyRecent(own bool) []common.Commitment {
	id := "other"
	if own {
		id = cudOwnName(0)
	}
	return []common.Commitment{{
		Provider: common.ProviderGCP, Service: common.ServiceCompute, CommitmentType: common.CommitmentCUD,
		Account: cudProject, Region: cudRegion, State: common.CommitmentStateActive,
		StartDate: time.Now().Add(-time.Hour), ResourceType: familyN2, CommitmentID: id,
	}}
}

func gcpGuardRec() common.Recommendation {
	return common.Recommendation{Provider: common.ProviderGCP, Service: common.ServiceCompute, Region: cudRegion,
		ResourceType: "n2-standard-4", Details: &common.ComputeDetails{GCPCommitmentType: familyN2}}
}

func TestGCPRecentRefusal_ForwardsTheGCPFilterWithTheListedProject(t *testing.T) {
	inner := new(MockServiceClient)
	inner.On("GetExistingCommitments", mock.Anything).Return(spyRecent(false), nil)
	spy := &filterSpy{MockServiceClient: inner, filtered: true}

	err := gcpRecentCommitmentRefusal(context.Background(), spy, gcpGuardRec(), nil, "")

	require.Error(t, err)
	assert.Equal(t, gcpRecentCommitmentNote, err.Error())
	var notSubmitted *beforeSubmissionError
	assert.True(t, errors.As(err, &notSubmitted))
	require.Equal(t, 1, spy.calls, "the wrapper forwarded the provider filter")
	assert.Equal(t, cudProject, spy.gotRecs[0].Account, "project stamped from the listed commitments")
	assert.Equal(t, common.CommitmentCUD, spy.gotRecs[0].CommitmentType)
}

func TestGCPRecentRefusal_DropsOwnCommitmentsBeforeFiltering(t *testing.T) {
	inner := new(MockServiceClient)
	inner.On("GetExistingCommitments", mock.Anything).Return(spyRecent(true), nil)
	spy := &filterSpy{MockServiceClient: inner, filtered: true}

	require.NoError(t, gcpRecentCommitmentRefusal(context.Background(), spy, gcpGuardRec(), map[string]bool{cudOwnName(0): true}, ""))
	assert.Zero(t, spy.calls, "nothing recent and foreign is left, so the filter is not reached")
}

// A recent CUD bought by another CUDly lineage (another plan step, a
// re-approval, or a sibling account sharing the project) carries a valid
// CUDly-shaped name that is not this execution's, so it must count as a recent
// commitment and refuse the rec, not be mistaken for an own commitment.
func TestGCPRecentRefusal_OtherLineageCUDIsRefused(t *testing.T) {
	recent := spyRecent(false)
	recent[0].CommitmentID = gcpCommitmentName(common.DeriveIdempotencyToken("other-lineage", 0))
	inner := new(MockServiceClient)
	inner.On("GetExistingCommitments", mock.Anything).Return(recent, nil)
	spy := &filterSpy{MockServiceClient: inner, filtered: true}

	err := gcpRecentCommitmentRefusal(context.Background(), spy, gcpGuardRec(), map[string]bool{cudOwnName(0): true}, "")

	require.Error(t, err)
	assert.Equal(t, gcpRecentCommitmentNote, err.Error())
	var notSubmitted *beforeSubmissionError
	assert.True(t, errors.As(err, &notSubmitted))
	require.Equal(t, 1, spy.calls, "the other lineage's CUD reached the provider filter as a recent commitment")
	inner.AssertNotCalled(t, "PurchaseCommitment", mock.Anything, mock.Anything, mock.Anything)
}

// A lost-response retry whose own commitment is already listed must reach the
// purchase call, which adopts it, even when another lineage's recent CUD in the
// same family is listed too; refusing it would record no submission for a rec
// whose commitment exists.
func TestGCPRecentRefusal_OwnCommitmentListedAdoptsDespiteForeignCUD(t *testing.T) {
	recent := append(spyRecent(true), spyRecent(false)...)
	recent[1].CommitmentID = gcpCommitmentName(common.DeriveIdempotencyToken("other-lineage", 0))
	inner := new(MockServiceClient)
	inner.On("GetExistingCommitments", mock.Anything).Return(recent, nil)
	spy := &filterSpy{MockServiceClient: inner, filtered: true}

	err := gcpRecentCommitmentRefusal(context.Background(), spy, gcpGuardRec(), map[string]bool{cudOwnName(0): true}, cudOwnName(0))

	require.NoError(t, err)
}

// When this rec's own commitment is listed, adoption wins even if the check
// would otherwise fail: refusing there records no_submission for a rec whose
// commitment exists. So the own-commitment case is decided before any error.
func TestGCPRecentRefusal_OwnCommitmentListedWinsOverACheckerFailure(t *testing.T) {
	foreign := gcpCommitmentName(common.DeriveIdempotencyToken("other-lineage", 0))
	for name, build := range map[string]func() (provider.ServiceClient, []common.Commitment){
		"client without the recent filter": func() (provider.ServiceClient, []common.Commitment) {
			recent := append(spyRecent(true), spyRecent(false)...)
			recent[1].CommitmentID = foreign
			inner := new(MockServiceClient)
			inner.On("GetExistingCommitments", mock.Anything).Return(recent, nil)
			return inner, recent
		},
		"recent commitments from two projects": func() (provider.ServiceClient, []common.Commitment) {
			recent := append(spyRecent(true), spyRecent(false)...)
			recent[1].CommitmentID = foreign
			recent[1].Account = "other-project"
			inner := new(MockServiceClient)
			inner.On("GetExistingCommitments", mock.Anything).Return(recent, nil)
			return &filterSpy{MockServiceClient: inner, filtered: true}, recent
		},
	} {
		t.Run(name, func(t *testing.T) {
			client, _ := build()
			err := gcpRecentCommitmentRefusal(context.Background(), client, gcpGuardRec(), map[string]bool{cudOwnName(0): true}, cudOwnName(0))
			require.NoError(t, err)
		})
	}
}

// Another rec of the same execution owning a listed commitment does not let
// this rec past a foreign recent CUD: only this rec's own name defers.
func TestGCPRecentRefusal_SiblingRecOwnNameDoesNotDefer(t *testing.T) {
	recent := append(spyRecent(true), spyRecent(false)...)
	recent[1].CommitmentID = gcpCommitmentName(common.DeriveIdempotencyToken("other-lineage", 0))
	inner := new(MockServiceClient)
	inner.On("GetExistingCommitments", mock.Anything).Return(recent, nil)
	spy := &filterSpy{MockServiceClient: inner, filtered: true}

	err := gcpRecentCommitmentRefusal(context.Background(), spy, gcpGuardRec(), map[string]bool{cudOwnName(0): true, cudOwnName(1): true}, cudOwnName(1))

	require.Error(t, err)
	assert.Equal(t, gcpRecentCommitmentNote, err.Error())
}

// Recent commitments from two projects cannot all describe this rec's project,
// so the check fails closed instead of stamping the first project and letting
// a duplicate in the other one through.
func TestGCPRecentRefusal_CommitmentsFromTwoProjectsFailClosed(t *testing.T) {
	recent := append(spyRecent(false), spyRecent(false)...)
	recent[1].Account = "other-project"
	recent[1].CommitmentID = "other-2"
	inner := new(MockServiceClient)
	inner.On("GetExistingCommitments", mock.Anything).Return(recent, nil)
	spy := &filterSpy{MockServiceClient: inner, filtered: false}

	err := gcpRecentCommitmentRefusal(context.Background(), spy, gcpGuardRec(), nil, "")

	require.Error(t, err)
	assert.Equal(t, gcpDuplicateCheckFailedNote, err.Error())
	var notSubmitted *beforeSubmissionError
	assert.True(t, errors.As(err, &notSubmitted))
	assert.Zero(t, spy.calls, "the provider filter is never reached with a mixed-project list")
}

// A client without the GCP filter must fail closed, never reach recfilter's
// generic map path.
func TestGCPRecentRefusal_ClientWithoutFilterFailsClosed(t *testing.T) {
	inner := new(MockServiceClient)
	inner.On("GetExistingCommitments", mock.Anything).Return(spyRecent(false), nil)

	err := gcpRecentCommitmentRefusal(context.Background(), inner, gcpGuardRec(), nil, "")

	require.Error(t, err)
	assert.Equal(t, gcpDuplicateCheckFailedNote, err.Error())
}

func TestGCPRecentRefusal_OtherRecsNeverTouchTheClient(t *testing.T) {
	client := new(MockServiceClient) // any call panics: no expectations registered
	for _, rec := range []common.Recommendation{
		{Provider: common.ProviderAWS, Service: common.ServiceEC2},
		{Provider: common.ProviderAzure, Service: common.ServiceCompute},
		{Provider: common.ProviderGCP, Service: common.ServiceRelationalDB},
	} {
		assert.NoError(t, gcpRecentCommitmentRefusal(context.Background(), client, rec, nil, ""), "%s/%s", rec.Provider, rec.Service)
	}
}
