package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// Coverage for the ladder arming gate (platform#118 PR2): turning
// LadderExecutionEnabled on, or arming / raising a ladder_config, needs
// execute:purchases for a real user. Admin without the verb and the admin API
// key are refused; de-escalations stay ordinary. Reuses the apg* helpers of
// auto_purchase_grant_test.go.

const lgAcct = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"

func lgStored(enabled bool, mode string, capUSD *float64) *config.LadderConfigDB {
	return &config.LadderConfigDB{ID: "cfg-1", CloudAccountID: lgAcct, Provider: "aws", Enabled: enabled, Mode: mode, MaxHourlyCommitPerRun: capUSD}
}

func lgBody(enabled bool, mode, capJSON string) string {
	capField := ""
	if capJSON != "" {
		capField = `"max_hourly_commit_per_run":` + capJSON + `,`
	}
	return fmt.Sprintf(`{"cloud_account_id":"%s","provider":"aws","enabled":%t,"mode":"%s","cadence":"daily",%s%s}`,
		lgAcct, enabled, mode, capField, ladderValidRamp)
}

func lgCall(t *testing.T, caller apgCaller, stored *config.LadderConfigDB, body string) (*MockConfigStore, error) {
	t.Helper()
	return lgCallFor(t, "aws", caller, stored, body)
}

func lgCallFor(t *testing.T, provider string, caller apgCaller, stored *config.LadderConfigDB, body string) (*MockConfigStore, error) {
	t.Helper()
	store := new(MockConfigStore)
	t.Cleanup(func() { store.AssertExpectations(t) })
	// Maybe: a body that fails validation is refused before the stored config
	// is read; callers assert the outcome through the error and the upsert.
	store.On("GetLadderConfig", mock.Anything, lgAcct, provider).Return(stored, nil).Maybe()
	store.On("UpsertLadderConfig", mock.Anything, mock.Anything).Return(&config.LadderConfigDB{ID: "cfg-1"}, nil).Maybe()
	store.GetCloudAccountFn = func(_ context.Context, id string) (*config.CloudAccount, error) {
		return &config.CloudAccount{ID: id, Provider: provider}, nil
	}
	h, req := caller.handlerAndReq(store, body)
	_, err := h.upsertLadderConfig(context.Background(), req)
	return store, err
}

func TestLadderArmingGate_ConfigWrites(t *testing.T) {
	t.Parallel()
	admin := apgCaller{perms: apgAdminOnly}
	granted := apgCaller{perms: apgAdminPlus}
	key := apgCaller{key: true}
	auto, email := "auto_approve", "email_approval"
	cap1, cap03 := apgCap(1), apgCap(0.3)

	tests := []struct {
		stored      *config.LadderConfigDB
		name        string
		body        string
		caller      apgCaller
		wantRefused bool
	}{
		{name: "create armed: admin refused", caller: admin, stored: nil, body: lgBody(true, auto, "1"), wantRefused: true},
		{name: "create armed: admin API key refused", caller: key, stored: nil, body: lgBody(true, auto, "1"), wantRefused: true},
		{name: "create armed: execute:purchases allowed", caller: granted, stored: nil, body: lgBody(true, auto, "1"), wantRefused: false},
		{name: "create disabled auto: admin allowed", caller: admin, stored: nil, body: lgBody(false, auto, "1"), wantRefused: false},
		{name: "create enabled email_approval: admin allowed", caller: admin, stored: nil, body: lgBody(true, email, "1"), wantRefused: false},
		{name: "enable a disabled auto config: admin refused", caller: admin, stored: lgStored(false, auto, cap1), body: lgBody(true, auto, "1"), wantRefused: true},
		{name: "enable a disabled auto config: admin API key refused", caller: key, stored: lgStored(false, auto, cap1), body: lgBody(true, auto, "1"), wantRefused: true},
		{name: "email_approval to auto: admin refused", caller: admin, stored: lgStored(true, email, cap1), body: lgBody(true, auto, "1"), wantRefused: true},
		{name: "email_approval to auto: execute:purchases allowed", caller: granted, stored: lgStored(true, email, cap1), body: lgBody(true, auto, "1"), wantRefused: false},
		{name: "raise cap: admin refused", caller: admin, stored: lgStored(true, auto, cap1), body: lgBody(true, auto, "2"), wantRefused: true},
		{name: "raise cap: admin API key refused", caller: key, stored: lgStored(true, auto, cap1), body: lgBody(true, auto, "2"), wantRefused: true},
		{name: "raise cap: execute:purchases allowed", caller: granted, stored: lgStored(true, auto, cap1), body: lgBody(true, auto, "2"), wantRefused: false},
		{name: "raise by one micro: admin refused", caller: admin, stored: lgStored(true, auto, cap1), body: lgBody(true, auto, "1.000001"), wantRefused: true},
		{name: "same cap: admin allowed", caller: admin, stored: lgStored(true, auto, cap1), body: lgBody(true, auto, "1"), wantRefused: false},
		{name: "same cap 0.3 (binary-float trap): admin allowed", caller: admin, stored: lgStored(true, auto, cap03), body: lgBody(true, auto, "0.30"), wantRefused: false},
		{name: "lower cap: admin API key allowed", caller: key, stored: lgStored(true, auto, cap1), body: lgBody(true, auto, "0.5"), wantRefused: false},
		{name: "set a cap on an uncapped armed config: admin API key allowed", caller: key, stored: lgStored(true, auto, nil), body: lgBody(true, auto, "5"), wantRefused: false},
		{name: "disarm (disable): admin API key allowed", caller: key, stored: lgStored(true, auto, cap1), body: lgBody(false, auto, "1"), wantRefused: false},
		{name: "switch to email_approval: admin API key allowed", caller: key, stored: lgStored(true, auto, cap1), body: lgBody(true, email, "1"), wantRefused: false},
		{name: "raise cap while disabled: admin allowed", caller: admin, stored: lgStored(false, auto, cap1), body: lgBody(false, auto, "9"), wantRefused: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, err := lgCall(t, tc.caller, tc.stored, tc.body)
			if tc.wantRefused {
				apgRefused(t, err)
				assert.False(t, apgCalled(store, "UpsertLadderConfig"), "a refused write must not reach the store")
				return
			}
			require.NoError(t, err)
			assert.True(t, apgCalled(store, "UpsertLadderConfig"))
		})
	}
}

func TestLadderArmingGate_ConfigLookupErrorFailsClosed(t *testing.T) {
	t.Parallel()
	store := new(MockConfigStore)
	t.Cleanup(func() { store.AssertExpectations(t) })
	store.On("GetLadderConfig", mock.Anything, lgAcct, "aws").Return((*config.LadderConfigDB)(nil), fmt.Errorf("db down"))
	store.GetCloudAccountFn = func(_ context.Context, id string) (*config.CloudAccount, error) {
		return &config.CloudAccount{ID: id, Provider: "aws"}, nil
	}
	h, req := apgCaller{perms: apgAdminPlus}.handlerAndReq(store, lgBody(true, "auto_approve", "1"))
	_, err := h.upsertLadderConfig(context.Background(), req)
	require.Error(t, err)
	assert.False(t, apgCalled(store, "UpsertLadderConfig"))
}

// Clearing or omitting the cap is still legal for non-AWS providers, so the
// gate's clear-cap rules stay pinned here with gcp (the gate is
// provider-agnostic). AWS can no longer reach them (#1815).
func TestLadderArmingGate_ClearCapNonAWS(t *testing.T) {
	t.Parallel()
	admin := apgCaller{perms: apgAdminOnly}
	key := apgCaller{key: true}
	cap1 := apgCap(1)
	gcpStored := func(c *float64) *config.LadderConfigDB {
		s := lgStored(true, "auto_approve", c)
		s.Provider = "gcp"
		return s
	}
	gcpBody := func() string {
		return fmt.Sprintf(`{"cloud_account_id":"%s","provider":"gcp","enabled":true,"mode":"auto_approve","cadence":"daily",%s}`, lgAcct, ladderValidRamp)
	}
	tests := []struct {
		stored      *config.LadderConfigDB
		name        string
		caller      apgCaller
		wantRefused bool
	}{
		{name: "clear cap: admin refused", caller: admin, stored: gcpStored(cap1), wantRefused: true},
		{name: "clear cap: admin API key refused", caller: key, stored: gcpStored(cap1), wantRefused: true},
		{name: "uncapped stays uncapped: admin allowed", caller: admin, stored: gcpStored(nil), wantRefused: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, err := lgCallFor(t, "gcp", tc.caller, tc.stored, gcpBody())
			if tc.wantRefused {
				apgRefused(t, err)
				assert.False(t, apgCalled(store, "UpsertLadderConfig"))
				return
			}
			require.NoError(t, err)
			assert.True(t, apgCalled(store, "UpsertLadderConfig"))
		})
	}
}

// Clearing the cap on an AWS config is now a 400 at validation (#1815), before
// the arming gate is consulted, so it can no longer be a 403 case above.
func TestLadderArmingGate_ClearingAWSCapIsValidationError(t *testing.T) {
	t.Parallel()
	store, err := lgCall(t, apgCaller{perms: apgAdminPlus}, lgStored(true, "auto_approve", apgCap(1)), lgBody(true, "auto_approve", ""))
	require.Error(t, err)
	ce, ok := IsClientError(err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, 400, ce.code)
	assert.Contains(t, ce.message, "max_hourly_commit_per_run")
	assert.False(t, apgCalled(store, "UpsertLadderConfig"))
}

func TestLadderArmingGate_CapValidation(t *testing.T) {
	t.Parallel()
	for _, capJSON := range []string{"0", "-1", "0.0000004", "1.1234567", "1e13", "1e308", `"5"`} {
		t.Run(capJSON, func(t *testing.T) {
			store, err := lgCall(t, apgCaller{perms: apgAdminPlus}, nil, lgBody(false, "auto_approve", capJSON))
			require.Error(t, err)
			ce, ok := IsClientError(err)
			require.True(t, ok, "%v", err)
			assert.Equal(t, 400, ce.code)
			assert.False(t, apgCalled(store, "UpsertLadderConfig"))
		})
	}
	// Boundary values stay valid.
	for _, capJSON := range []string{"0.000001", "123.456789", "1e12"} {
		_, err := lgCall(t, apgCaller{perms: apgAdminPlus}, nil, lgBody(false, "auto_approve", capJSON))
		require.NoError(t, err, capJSON)
	}
}

func TestLadderArmingGate_ExecutionSwitch(t *testing.T) {
	t.Parallel()
	off, on := baseValidConfig(), baseValidConfig()
	on.LadderExecutionEnabled = true
	const flip = `{"ladder_execution_enabled":true}`

	t.Run("off to on: admin without grant refused", func(t *testing.T) {
		saved, err := apgConfigCall(t, apgCaller{perms: apgAdminOnly}, off, flip)
		apgRefused(t, err)
		assert.Nil(t, saved)
	})
	t.Run("off to on: admin API key refused", func(t *testing.T) {
		saved, err := apgConfigCall(t, apgCaller{key: true}, off, flip)
		apgRefused(t, err)
		assert.Nil(t, saved)
	})
	t.Run("off to on: execute:purchases allowed and stamped", func(t *testing.T) {
		before := time.Now().UTC().Add(-time.Second)
		saved, err := apgConfigCall(t, apgCaller{perms: apgAdminPlus}, off, flip)
		require.NoError(t, err)
		require.NotNil(t, saved)
		assert.True(t, saved.LadderExecutionEnabled)
		require.NotNil(t, saved.LadderExecutionChangedBy)
		assert.Equal(t, apgUserID, *saved.LadderExecutionChangedBy)
		require.NotNil(t, saved.LadderExecutionChangedVia)
		assert.Equal(t, string(PrincipalSession), *saved.LadderExecutionChangedVia)
		require.NotNil(t, saved.LadderExecutionChangedAt)
		assert.True(t, saved.LadderExecutionChangedAt.After(before))
		assert.Nil(t, saved.AutoPurchaseChangedBy, "the auto-purchase stamp is untouched")
	})
	t.Run("on to off: admin API key allowed and stamped", func(t *testing.T) {
		saved, err := apgConfigCall(t, apgCaller{key: true}, on, `{"ladder_execution_enabled":false}`)
		require.NoError(t, err)
		require.NotNil(t, saved)
		assert.False(t, saved.LadderExecutionEnabled)
		require.NotNil(t, saved.LadderExecutionChangedVia)
		assert.Equal(t, string(PrincipalAdminAPIKey), *saved.LadderExecutionChangedVia)
	})
	t.Run("unchanged switch: admin may save other settings, no stamp", func(t *testing.T) {
		saved, err := apgConfigCall(t, apgCaller{perms: apgAdminOnly}, on, `{"ladder_execution_enabled":true,"default_coverage":70}`)
		require.NoError(t, err)
		require.NotNil(t, saved)
		assert.Nil(t, saved.LadderExecutionChangedBy)
	})
	t.Run("laddering_enabled (planning only) needs no grant", func(t *testing.T) {
		saved, err := apgConfigCall(t, apgCaller{key: true}, off, `{"laddering_enabled":true}`)
		require.NoError(t, err)
		require.NotNil(t, saved)
		assert.True(t, saved.LadderingEnabled)
		assert.Nil(t, saved.LadderExecutionChangedBy)
	})
	t.Run("both switches flipped without grant: refused", func(t *testing.T) {
		saved, err := apgConfigCall(t, apgCaller{perms: apgAdminOnly}, off, `{"ladder_execution_enabled":true,"auto_purchase_enabled":false}`)
		apgRefused(t, err)
		assert.Nil(t, saved)
	})
}

func TestLadderArmingGate_StampsCannotBeForgedViaConfigBody(t *testing.T) {
	t.Parallel()
	stored := baseValidConfig()
	by, via := "real-actor", "user"
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	stored.LadderExecutionChangedBy, stored.LadderExecutionChangedVia, stored.LadderExecutionChangedAt = &by, &via, &at

	for _, body := range []string{
		`{"ladder_execution_changed_by":"mallory","ladder_execution_changed_via":"x","ladder_execution_changed_at":"2030-01-01T00:00:00Z"}`,
		`{"LadderExecutionChangedBy":"mallory","LadderExecutionChangedVia":"x","LadderExecutionChangedAt":"2030-01-01T00:00:00Z"}`,
	} {
		saved, err := apgConfigCall(t, apgCaller{perms: apgAdminOnly}, stored, body)
		require.NoError(t, err, body)
		require.NotNil(t, saved, body)
		require.NotNil(t, saved.LadderExecutionChangedBy, body)
		assert.Equal(t, "real-actor", *saved.LadderExecutionChangedBy, body)
		assert.True(t, saved.LadderExecutionChangedAt.Equal(at), body)
	}
}
