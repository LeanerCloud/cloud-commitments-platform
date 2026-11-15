//go:build integration

package api

// Real PostgreSQL: the per-sweep ceiling through the ladder config handlers
// (issue #70, slice M2), including the arming grant and an unmigrated schema.

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type ladderCeilingRig struct {
	pg    *testhelpers.PostgresContainer
	store *config.PostgresStore
	acct  string
}

func newLadderCeilingRig(t *testing.T) *ladderCeilingRig {
	t.Helper()
	pg, store := setupSubmitDB(t)
	a := &config.CloudAccount{Name: "ceiling", Provider: "aws", ExternalID: "123456789012", Enabled: true}
	require.NoError(t, store.CreateCloudAccount(context.Background(), a))
	return &ladderCeilingRig{pg: pg, store: store, acct: a.ID}
}

func (r *ladderCeilingRig) handler(c apgCaller) (*Handler, func(body string) *events.LambdaFunctionURLRequest) {
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateSession", mock.Anything, "tok").Return(&Session{UserID: apgUserID}, nil).Maybe()
	mockAuth.grantPermissions(c.perms)
	mockAuth.On("GetAllowedAccountsAPI", mock.Anything, mock.Anything).Return([]string{"*"}, nil).Maybe()
	return &Handler{config: r.store, auth: mockAuth}, func(body string) *events.LambdaFunctionURLRequest {
		return &events.LambdaFunctionURLRequest{Headers: map[string]string{"Authorization": "Bearer tok"}, Body: body}
	}
}

func (r *ladderCeilingRig) put(c apgCaller, ceilingJSON string) error {
	h, req := r.handler(c)
	body := `{"cloud_account_id":"` + r.acct + `","provider":"aws","enabled":true,` +
		`"mode":"auto_approve","cadence":"daily","max_hourly_commit_per_run":1,` + ladderValidRamp
	if ceilingJSON != "" {
		body += `,"max_hourly_commit_per_sweep":` + ceilingJSON
	}
	_, err := h.upsertLadderConfig(context.Background(), req(body+"}"))
	return err
}

func (r *ladderCeilingRig) listed(t *testing.T) *float64 {
	t.Helper()
	h, req := r.handler(apgCaller{perms: apgAdminOnly})
	out, err := h.getLadderConfigs(context.Background(), req(""))
	require.NoError(t, err)
	cfgs := out.(map[string]any)["configs"].([]config.LadderConfigDB)
	require.Len(t, cfgs, 1)
	return cfgs[0].MaxHourlyCommitPerSweep
}

func TestLadderSweepCeiling_Integration_RoundTripAndGrant(t *testing.T) {
	r := newLadderCeilingRig(t)
	admin, granted := apgCaller{perms: apgAdminOnly}, apgCaller{perms: apgAdminPlus}

	// Every armed config created before the migration has NULL, which means
	// zero spend: the first value is a raise and needs the grant.
	require.NoError(t, r.put(granted, ""))
	apgRefused(t, r.put(admin, "1e12"))
	apgRefused(t, r.put(apgCaller{key: true}, "1e12"))
	assert.Nil(t, r.listed(t), "refused NULL -> value leaves the row NULL")
	require.NoError(t, r.put(granted, "5.25"))
	got := r.listed(t)
	require.NotNil(t, got)
	assert.Equal(t, 5.25, *got)

	// Raising on the armed config needs the grant; the row stays.
	apgRefused(t, r.put(admin, "6"))
	got = r.listed(t)
	require.NotNil(t, got)
	assert.Equal(t, 5.25, *got)

	// Lowering and omitting the key are ordinary writes; omitting keeps it.
	require.NoError(t, r.put(admin, "2"))
	require.NoError(t, r.put(admin, ""))
	got = r.listed(t)
	require.NotNil(t, got)
	assert.Equal(t, 2.0, *got)

	// Clearing lowers spend to zero, so it is an ordinary write.
	require.NoError(t, r.put(admin, "null"))
	assert.Nil(t, r.listed(t))
}

func TestLadderSweepCeiling_Integration_UnmigratedSchema(t *testing.T) {
	r := newLadderCeilingRig(t)
	require.NoError(t, r.put(apgCaller{perms: apgAdminPlus}, "5"))
	_, err := r.pg.DB.Pool().Exec(context.Background(), `UPDATE schema_migrations SET version = 164`)
	require.NoError(t, err)

	err = r.put(apgCaller{perms: apgAdminPlus}, "9")
	ce, ok := IsClientError(err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, 503, ce.code)
	require.NoError(t, r.put(apgCaller{perms: apgAdminPlus}, ""), "config writes keep working without the key")
	assert.Nil(t, r.listed(t), "unmigrated schema lists no ceiling")
}
