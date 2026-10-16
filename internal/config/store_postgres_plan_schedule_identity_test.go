package config

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestValidatePlanScheduleIdentityRejectsMalformedBoundaryInput(t *testing.T) {
	digest, id := "sha256:"+strings.Repeat("a", 64), uuid.NewString()
	upper, short := strings.ToUpper(id), "sha256:"+strings.Repeat("a", 63)
	valid := func() PlanScheduleCommandIdentity {
		return PlanScheduleCommandIdentity{CommandID: uuid.NewString(), PlanID: uuid.NewString(), RequestHash: digest,
			Actor: PlanScheduleActor{Kind: PlanScheduleAdminActor}, ProposedProviders: []string{"aws"}}
	}
	require.NoError(t, validatePlanScheduleIdentity(valid()))
	edit := valid()
	edit.ExpectedRevision, edit.Actor, edit.ProposedAccountIDs = &digest, PlanScheduleActor{Kind: PlanScheduleUserKeyActor, UserID: &id, KeyID: &id}, []string{uuid.NewString(), id}
	require.NoError(t, validatePlanScheduleIdentity(edit))
	actor := func(kind PlanScheduleActorKind, user, key *string) func(*PlanScheduleCommandIdentity) {
		return func(i *PlanScheduleCommandIdentity) {
			i.Actor = PlanScheduleActor{Kind: kind, UserID: user, KeyID: key}
		}
	}
	cases := map[string]func(*PlanScheduleCommandIdentity){
		"command id": func(i *PlanScheduleCommandIdentity) { i.CommandID = upper }, "plan id": func(i *PlanScheduleCommandIdentity) { i.PlanID = "x" },
		"uppercase hash":    func(i *PlanScheduleCommandIdentity) { i.RequestHash = "sha256:" + strings.Repeat("A", 64) },
		"short revision":    func(i *PlanScheduleCommandIdentity) { i.ExpectedRevision = &short },
		"duplicate account": func(i *PlanScheduleCommandIdentity) { i.ProposedAccountIDs = []string{id, id} },
		"uppercase account": func(i *PlanScheduleCommandIdentity) { i.ProposedAccountIDs = []string{upper} },
		"empty provider":    func(i *PlanScheduleCommandIdentity) { i.ProposedProviders = []string{"aws", ""} },
		"actor user id":     actor(PlanScheduleSessionActor, &upper, nil), "actor key id": actor(PlanScheduleUserKeyActor, &id, &upper),
		"unknown actor": actor("root", nil, nil), "session no user": actor(PlanScheduleSessionActor, nil, nil),
		"user key no key": actor(PlanScheduleUserKeyActor, &id, nil), "admin with user": actor(PlanScheduleAdminActor, &id, nil),
		"admin with key": actor(PlanScheduleAdminActor, nil, &id), "session with key": actor(PlanScheduleSessionActor, &id, &id),
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			identity := valid()
			mutate(&identity)
			require.ErrorIs(t, validatePlanScheduleIdentity(identity), ErrPurchasePlanConflict)
		})
	}
}

func TestValidatePlanScheduleLockedTargetComparesAccountSetsOrderIndependently(t *testing.T) {
	a, b, c := uuid.NewString(), uuid.NewString(), uuid.NewString()
	locked := []PlanEditorAccount{{ID: a}, {ID: b}}
	check := func(ids, scope []string) error {
		return validatePlanScheduleLockedTarget(locked, &PlanScheduleMutation{AccountIDs: ids, Scope: PlanScheduleScope{AccountIDs: scope}})
	}
	require.NoError(t, check([]string{b, a}, []string{b, a}))
	require.NoError(t, check(nil, []string{a, b}))
	for name, args := range map[string][2][]string{"scope missing a locked account": {{a}, {a}}, "scope has unlocked account": {{a}, {a, b, c}},
		"mutation has unlocked account": {{c}, {a, b}}, "duplicate mutation account": {{a, a}, {a, b}},
		"duplicate scope account": {{a}, {a, a}}, "non-canonical scope id": {{a}, {strings.ToUpper(a), b}},
	} {
		require.Error(t, check(args[0], args[1]), name)
	}
}
