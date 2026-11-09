//go:build integration

package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// [fail-first] The distinct pairs read behind the handler's authorization.
func TestPostgresStoreDB_RIExchangeDirectList_AccountRegionPairs(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	a1 := strPtr("acc-1")
	env.insertDirect(t, directSpec{account: a1})
	env.insertDirect(t, directSpec{account: a1, status: "submitted"})
	env.insertDirect(t, directSpec{account: nil, region: "us-east-1"})
	env.insertDirect(t, directSpec{account: a1, resolved: true, region: "ap-south-1"})
	env.insertDirect(t, directSpec{provider: "azure", account: a1})
	env.insertDirect(t, directSpec{status: "completed", account: strPtr("acc-9"), region: "sa-east-1"})

	pairs, err := env.storeA.ListUnresolvedRIExchangeDirectAccountRegions(env.ctx)
	require.NoError(t, err)
	require.Len(t, pairs, 2)
	assert.Nil(t, pairs[0].CloudAccountID)
	assert.Equal(t, "us-east-1", pairs[0].Region)
	assert.Equal(t, "acc-1", *pairs[1].CloudAccountID)
	assert.Equal(t, "eu-west-1", pairs[1].Region)
}

// [fail-first] Get returns an AWS row of any status, and not found for Azure or
// a missing id.
func TestPostgresStoreDB_GetRIExchangeDirectExecution(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	acc := strPtr("acc-1")
	resolved := env.insertDirect(t, directSpec{account: acc, resolved: true, errText: "raw provider text"})
	az := env.insertDirect(t, directSpec{provider: "azure"})

	got, err := env.storeA.GetRIExchangeDirectExecution(env.ctx, resolved)
	require.NoError(t, err)
	assert.Equal(t, resolved, got.ID)
	assert.Equal(t, "unknown", got.Status)
	assert.Equal(t, "eu-west-1", got.Region)
	assert.Equal(t, "acc-1", *got.CloudAccountID)
	assert.Equal(t, "100", got.MaxPaymentDue)
	assert.Equal(t, "raw provider text", got.Error)
	assert.NotNil(t, got.ResolvedAt)
	assert.NotNil(t, got.CompletedAt)
	assert.JSONEq(t, `[{"offering_id":"o","count":1}]`, string(got.Targets))

	_, err = env.storeA.GetRIExchangeDirectExecution(env.ctx, az)
	require.ErrorIs(t, err, ErrRIExchangeResolveNotFound)
	_, err = env.storeA.GetRIExchangeDirectExecution(env.ctx, uuid.NewString())
	require.ErrorIs(t, err, ErrRIExchangeResolveNotFound)
}

// The read type never carries who executed through which credential.
func TestRIExchangeDirectRow_HasNoCredentialFields(t *testing.T) {
	typ := reflect.TypeOf(RIExchangeDirectRow{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		assert.NotContains(t, name, "userid", typ.Field(i).Name)
		assert.NotContains(t, name, "apikey", typ.Field(i).Name)
	}
}
