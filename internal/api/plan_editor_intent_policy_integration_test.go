//go:build integration

package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/require"
)

func TestPlanEditorIntentPolicy(t *testing.T) {
	f := newEditorReadFixture(t)
	up := auth.Permission{Action: auth.ActionUpdate, Resource: auth.ResourcePlans}
	buy := auth.Permission{Action: auth.ActionUpdate, Resource: auth.ResourcePurchases}
	any := auth.Permission{Action: auth.ActionUpdateAny, Resource: auth.ResourcePurchases}
	revoke := auth.Permission{Action: auth.ActionRevokeAny, Resource: auth.ResourcePurchases}
	all := editorFieldPermissions(up, buy, any, revoke)
	run := func(t *testing.T, key []auth.Permission, prepare func(*config.PlanEditorSnapshot), build func(*config.PlanEditorSnapshot) *planEditorIntent) error {
		t.Helper()
		req, user, _ := f.credential(t, all, key, []string{"*"})
		snapshot := editorFieldSnapshot(t, f, user)
		if prepare != nil {
			prepare(snapshot)
		}
		session, err := f.h.requirePlanEditorReadSession(t.Context(), req)
		require.NoError(t, err)
		f.h.config = nil
		_, err = f.h.planEditorIntentPolicy(t.Context(), req, session, snapshot, build(snapshot), time.Now().UTC())
		return err
	}
	service := func(field string, approved bool) func(*config.PlanEditorSnapshot) *planEditorIntent {
		return func(s *config.PlanEditorSnapshot) *planEditorIntent {
			patch := editorPolicyServicePatch(s.Plan.Services["primary"], field, true)
			return &planEditorIntent{PlanScheduleIntent: config.PlanScheduleIntent{Services: map[string]config.PlanScheduleServicePatch{"primary": patch}, AllowApprovedEdits: approved}}
		}
	}

	t.Run("read-only credential: every supplied field is permission denied, whatever the plan state", func(t *testing.T) {
		started := func(s *config.PlanEditorSnapshot) { s.Plan.RampSchedule.CurrentStep = 1 }
		for _, prepare := range []func(*config.PlanEditorSnapshot){nil, started} {
			for _, field := range editorPolicyControlFields {
				err := run(t, editorFieldPermissions(), prepare, func(s *config.PlanEditorSnapshot) *planEditorIntent { return editorPolicyControl(s, field, true) })
				requireEditorPolicyRefusal(t, err, http.StatusForbidden, codePermissionDenied, "control "+field)
			}
			for _, field := range editorPolicyServiceFields {
				err := run(t, editorFieldPermissions(), prepare, service(field, true))
				requireEditorPolicyRefusal(t, err, http.StatusForbidden, codePermissionDenied, "service "+field)
			}
		}
	})
	t.Run("read-only mixed request answers 403 every time", func(t *testing.T) {
		mixed := func(s *config.PlanEditorSnapshot) *planEditorIntent {
			intent := editorPolicyControl(s, "target_accounts", true)
			intent.Plan = editorPolicyControl(s, "name", true).Plan
			return intent
		}
		started := func(s *config.PlanEditorSnapshot) { s.Plan.RampSchedule.CurrentStep = 1 }
		for range 100 {
			requireEditorPolicyRefusal(t, run(t, editorFieldPermissions(), started, mixed), http.StatusForbidden, codePermissionDenied, "mixed")
		}
	})
	t.Run("filters and min_count are locked on a scheduled plan, even with approved edits", func(t *testing.T) {
		for _, field := range []string{"include_engines", "exclude_engines", "include_regions", "exclude_regions", "include_types", "exclude_types", "min_count"} {
			for _, approved := range []bool{false, true} {
				err := run(t, all, nil, service(field, approved))
				requireEditorPolicyRefusal(t, err, http.StatusConflict, codeEditorFieldLocked, field)
			}
		}
	})
	t.Run("approved rows need allow_approved_edits for an ordinary service field", func(t *testing.T) {
		requireEditorPolicyRefusal(t, run(t, all, nil, service("coverage", false)), http.StatusConflict, codeEditorFieldLocked, "without consent")
		require.NoError(t, run(t, all, nil, service("coverage", true)))
	})
	t.Run("filters stay editable on a plan with no scheduled step", func(t *testing.T) {
		unscheduled := func(s *config.PlanEditorSnapshot) { s.Plan.RampSchedule.TotalSteps, s.Executions = 0, nil }
		require.NoError(t, run(t, all, unscheduled, service("include_regions", false)))
	})
	t.Run("a service key with no policy entry is denied", func(t *testing.T) {
		typo := func(s *config.PlanEditorSnapshot) *planEditorIntent {
			intent := service("coverage", true)(s)
			intent.Services["typo"] = intent.Services["primary"]
			delete(intent.Services, "primary")
			return intent
		}
		requireEditorPolicyRefusal(t, run(t, all, nil, typo), http.StatusConflict, codeEditorFieldLocked, "typo key")
	})
	t.Run("unchanged supplied values pass for a read-only credential", func(t *testing.T) {
		same := func(s *config.PlanEditorSnapshot) *planEditorIntent { return editorPolicyControl(s, "name", false) }
		require.NoError(t, run(t, editorFieldPermissions(), nil, same))
	})
}
