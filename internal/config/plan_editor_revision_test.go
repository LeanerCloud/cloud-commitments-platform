package config

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func editorRevisionFixture() *PlanEditorSnapshot {
	at := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	account := "account-a"
	return &PlanEditorSnapshot{
		Plan: &PurchasePlan{ID: "plan-a", Name: "plan", UpdatedAt: at,
			Services:     map[string]ServiceConfig{"compute": {Provider: "aws", Service: "ec2", Term: 1}},
			RampSchedule: RampSchedule{Type: "weekly", StartDate: at, TotalSteps: 4}},
		AccountIDs: []string{account},
		Accounts:   []PlanEditorAccount{{ID: account, Name: "account", Provider: "aws"}},
		Executions: []PurchaseExecution{{PlanID: "plan-a", ExecutionID: "execution-a", Status: "pending",
			ScheduledDate: at, ApprovalToken: "stored-digest", ApprovalTokenExpiresAt: &at,
			Recommendations: []RecommendationRecord{{ID: "rec-a", CloudAccountID: &account, Count: 2,
				Details: json.RawMessage(`{"z":2,"nested":{"b":2,"a":1}}`)}}}},
		Baseline:          PlanRampBaseline{"identity": {Count: 5}},
		Committed:         map[string]PlanRampQuantity{"identity": {Count: 2}},
		executionVersions: []planEditorExecutionVersion{{ID: "execution-a", UpdatedAt: at}},
	}
}

func TestPlanEditorRevision_CanonicalAndEquivalentCurrentState(t *testing.T) {
	t.Parallel()
	s := editorRevisionFixture()
	first, err := s.Revision()
	require.NoError(t, err)
	require.Regexp(t, `^sha256:[0-9a-f]{64}$`, first)
	other := editorRevisionFixture()
	other.Executions[0].Recommendations[0].Details = json.RawMessage(`{ "nested": { "a": 1, "b": 2 }, "z": 2 }`)
	second, err := other.Revision()
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, editorRevisionFixture(), s, "fingerprinting must not mutate captured state")
	s.Plan.Name = "temporary"
	s.executionVersions[0].UpdatedAt = s.executionVersions[0].UpdatedAt.Add(time.Second)
	s.Plan.Name = "plan"
	s.executionVersions = editorRevisionFixture().executionVersions
	restored, err := s.Revision()
	require.NoError(t, err)
	require.Equal(t, first, restored, "equivalent complete current state is not an event counter")
}

func TestPlanEditorRevision_PreservesRawNumberPrecision(t *testing.T) {
	t.Parallel()
	s := editorRevisionFixture()
	s.Executions[0].Recommendations[0].Details = json.RawMessage(`{"units":9007199254740992}`)
	first, err := s.Revision()
	require.NoError(t, err)
	s.Executions[0].Recommendations[0].Details = json.RawMessage(`{"units":9007199254740993}`)
	second, err := s.Revision()
	require.NoError(t, err)
	require.NotEqual(t, first, second, "distinct persisted integers above 2^53 must remain distinct")
}

func TestPlanEditorRevision_PreservesRecommendationOrder(t *testing.T) {
	t.Parallel()
	s := editorRevisionFixture()
	s.Executions[0].Recommendations = append(s.Executions[0].Recommendations, RecommendationRecord{ID: "rec-b"})
	first, err := s.Revision()
	require.NoError(t, err)
	recs := s.Executions[0].Recommendations
	recs[0], recs[1] = recs[1], recs[0]
	second, err := s.Revision()
	require.NoError(t, err)
	require.NotEqual(t, first, second, "persisted recommendation array order remains state")
}

func TestPlanEditorRevision_PersistedStateSensitivity(t *testing.T) {
	t.Parallel()
	base, err := editorRevisionFixture().Revision()
	require.NoError(t, err)
	for _, tc := range []struct {
		name   string
		change func(*PlanEditorSnapshot)
	}{
		{"plan ID", func(s *PlanEditorSnapshot) { s.Plan.ID += "-changed" }},
		{"plan version", func(s *PlanEditorSnapshot) { s.Plan.UpdatedAt = s.Plan.UpdatedAt.Add(time.Second) }},
		{"plan service term", func(s *PlanEditorSnapshot) {
			service := s.Plan.Services["compute"]
			service.Term = 3
			s.Plan.Services["compute"] = service
		}},
		{"ramp clock", func(s *PlanEditorSnapshot) {
			s.Plan.RampSchedule.StartDate = s.Plan.RampSchedule.StartDate.Add(time.Second)
		}},
		{"association", func(s *PlanEditorSnapshot) { s.AccountIDs = append(s.AccountIDs, "account-b") }},
		{"account ID", func(s *PlanEditorSnapshot) { s.Accounts[0].ID += "-changed" }},
		{"account name", func(s *PlanEditorSnapshot) { s.Accounts[0].Name += "-changed" }},
		{"account provider", func(s *PlanEditorSnapshot) { s.Accounts[0].Provider = "azure" }},
		{"execution ID", func(s *PlanEditorSnapshot) { s.Executions[0].ExecutionID += "-changed" }},
		{"execution status", func(s *PlanEditorSnapshot) { s.Executions[0].Status = StatusCompleted }},
		{"execution date", func(s *PlanEditorSnapshot) {
			s.Executions[0].ScheduledDate = s.Executions[0].ScheduledDate.Add(time.Second)
		}},
		{"approval digest", func(s *PlanEditorSnapshot) { s.Executions[0].ApprovalToken += "-changed" }},
		{"approval expiry", func(s *PlanEditorSnapshot) {
			at := s.Executions[0].ApprovalTokenExpiresAt.Add(time.Second)
			s.Executions[0].ApprovalTokenExpiresAt = &at
		}},
		{"persisted estimate", func(s *PlanEditorSnapshot) { s.Executions[0].EstimatedSavings = 10 }},
		{"recommendation ID", func(s *PlanEditorSnapshot) { s.Executions[0].Recommendations[0].ID += "-changed" }},
		{"recommendation snapshot", func(s *PlanEditorSnapshot) { s.Executions[0].Recommendations[0].Count++ }},
		{"raw details", func(s *PlanEditorSnapshot) {
			s.Executions[0].Recommendations[0].Details = json.RawMessage(`{"changed":true}`)
		}},
		{"baseline nil", func(s *PlanEditorSnapshot) { s.Baseline = nil }},
		{"baseline empty", func(s *PlanEditorSnapshot) { s.Baseline = PlanRampBaseline{} }},
		{"baseline quantity", func(s *PlanEditorSnapshot) { s.Baseline["identity"] = PlanRampQuantity{Count: 6} }},
		{"ledger quantity", func(s *PlanEditorSnapshot) { s.Committed["identity"] = PlanRampQuantity{Count: 3} }},
		{"execution version ID", func(s *PlanEditorSnapshot) { s.executionVersions[0].ID += "-changed" }},
		{"execution version", func(s *PlanEditorSnapshot) {
			s.executionVersions[0].UpdatedAt = s.executionVersions[0].UpdatedAt.Add(time.Second)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := editorRevisionFixture()
			tc.change(s)
			changed, revisionErr := s.Revision()
			require.NoError(t, revisionErr)
			require.NotEqual(t, base, changed)
		})
	}
	s := editorRevisionFixture()
	s.Baseline = nil
	nilBaseline, err := s.Revision()
	require.NoError(t, err)
	s.Baseline = PlanRampBaseline{}
	emptyBaseline, err := s.Revision()
	require.NoError(t, err)
	require.NotEqual(t, nilBaseline, emptyBaseline)
}

func TestPlanEditorRevision_InvalidStateFails(t *testing.T) {
	t.Parallel()
	invalid := editorRevisionFixture()
	invalid.Executions[0].Recommendations[0].Details = json.RawMessage(`{"broken":`)
	nonfinite := editorRevisionFixture()
	nonfinite.Executions[0].EstimatedSavings = math.Inf(1)
	for _, s := range []*PlanEditorSnapshot{nil, {}, invalid, nonfinite} {
		revision, err := s.Revision()
		require.Error(t, err)
		require.Empty(t, revision)
	}
}
