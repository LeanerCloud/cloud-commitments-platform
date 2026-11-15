package api

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// The per-sweep ceiling through the ladder config API: raising or clearing it
// on an armed config needs the auto-purchase grant like the per-run cap.

func lcBody(enabled bool, ceilingJSON string) string {
	field := ""
	if ceilingJSON != "" {
		field = `"max_hourly_commit_per_sweep":` + ceilingJSON + `,`
	}
	return fmt.Sprintf(`{"cloud_account_id":"%s","provider":"aws","enabled":%t,"mode":"auto_approve","cadence":"daily","max_hourly_commit_per_run":1,%s%s}`,
		lgAcct, enabled, field, ladderValidRamp)
}

func lcMicros(v int64) *int64 { return &v }

// lcCall runs the PUT with a stored armed config whose ceiling is storedMicros
// (getErr overrides the read).
func lcCall(t *testing.T, caller apgCaller, stored *config.LadderConfigDB, storedMicros *int64, getErr, setErr error, body string) (*MockConfigStore, error) {
	t.Helper()
	store := new(MockConfigStore)
	t.Cleanup(func() { store.AssertExpectations(t) })
	if stored != nil { // the handler fills the ceiling in place; keep subtests apart
		c := *stored
		stored = &c
	}
	store.On("GetLadderConfig", mock.Anything, lgAcct, "aws").Return(stored, nil).Maybe()
	store.On("GetLadderSweepCeiling", mock.Anything, "cfg-1").Return(storedMicros, getErr).Maybe()
	store.On("UpsertLadderConfig", mock.Anything, mock.Anything).Return(&config.LadderConfigDB{ID: "cfg-1"}, nil).Maybe()
	store.On("SetLadderSweepCeiling", mock.Anything, "cfg-1", mock.Anything).Return(setErr).Maybe()
	store.GetCloudAccountFn = func(_ context.Context, id string) (*config.CloudAccount, error) {
		return &config.CloudAccount{ID: id, Provider: "aws"}, nil
	}
	h, req := caller.handlerAndReq(store, body)
	_, err := h.upsertLadderConfig(context.Background(), req)
	return store, err
}

func lcSetValue(t *testing.T, store *MockConfigStore) *float64 {
	t.Helper()
	for _, c := range store.Calls {
		if c.Method == "SetLadderSweepCeiling" {
			return c.Arguments.Get(2).(*float64)
		}
	}
	t.Fatal("SetLadderSweepCeiling was not called")
	return nil
}

func TestLadderSweepCeiling_ArmingGate(t *testing.T) {
	t.Parallel()
	admin := apgCaller{perms: apgAdminOnly}
	granted := apgCaller{perms: apgAdminPlus}
	key := apgCaller{key: true}
	armed := lgStored(true, "auto_approve", apgCap(1))
	disabled := lgStored(false, "auto_approve", apgCap(1))
	five, tenth := lcMicros(5_000_000), lcMicros(300_000)

	tests := []struct {
		stored      *config.LadderConfigDB
		storedC     *int64
		name        string
		body        string
		caller      apgCaller
		wantRefused bool
		wantSet     bool
	}{
		{name: "raise: admin refused", caller: admin, stored: armed, storedC: five, body: lcBody(true, "10"), wantRefused: true},
		{name: "raise: API key refused", caller: key, stored: armed, storedC: five, body: lcBody(true, "10"), wantRefused: true},
		{name: "raise by one micro: admin refused", caller: admin, stored: armed, storedC: five, body: lcBody(true, "5.000001"), wantRefused: true},
		{name: "raise: execute:purchases allowed", caller: granted, stored: armed, storedC: five, body: lcBody(true, "10"), wantSet: true},
		// NULL means the sweep refuses, so clearing lowers spend to zero.
		{name: "clear: admin allowed", caller: admin, stored: armed, storedC: five, body: lcBody(true, "null"), wantSet: true},
		{name: "clear: API key allowed", caller: key, stored: armed, storedC: five, body: lcBody(true, "null"), wantSet: true},
		{name: "lower: API key allowed", caller: key, stored: armed, storedC: five, body: lcBody(true, "1"), wantSet: true},
		// NULL is zero spend: any value on an armed config is a raise.
		{name: "set from none: admin refused", caller: admin, stored: armed, storedC: nil, body: lcBody(true, "7"), wantRefused: true},
		{name: "set from none: API key refused", caller: key, stored: armed, storedC: nil, body: lcBody(true, "1e12"), wantRefused: true},
		{name: "set from none by one micro: admin refused", caller: admin, stored: armed, storedC: nil, body: lcBody(true, "0.000001"), wantRefused: true},
		{name: "set from none: execute:purchases allowed", caller: granted, stored: armed, storedC: nil, body: lcBody(true, "7"), wantSet: true},
		{name: "same value: admin allowed", caller: admin, stored: armed, storedC: five, body: lcBody(true, "5"), wantSet: true},
		{name: "same 0.3 (float trap): admin allowed", caller: admin, stored: armed, storedC: tenth, body: lcBody(true, "0.30"), wantSet: true},
		{name: "key omitted: unchanged, admin allowed", caller: admin, stored: armed, storedC: five, body: lcBody(true, "")},
		{name: "null on none: admin allowed, no write", caller: admin, stored: armed, storedC: nil, body: lcBody(true, "null")},
		{name: "raise while disabled: admin allowed", caller: admin, stored: disabled, storedC: five, body: lcBody(false, "10"), wantSet: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := lcCall(t, tc.caller, tc.stored, tc.storedC, nil, nil, tc.body)
			if tc.wantRefused {
				apgRefused(t, err)
				assert.False(t, apgCalled(store, "UpsertLadderConfig"), "a refused write must not reach the store")
				assert.False(t, apgCalled(store, "SetLadderSweepCeiling"))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantSet, apgCalled(store, "SetLadderSweepCeiling"))
		})
	}
}

func TestLadderSweepCeiling_RoundTripValues(t *testing.T) {
	t.Parallel()
	granted := apgCaller{perms: apgAdminPlus}
	store, err := lcCall(t, granted, lgStored(true, "auto_approve", apgCap(1)), lcMicros(5_000_000), nil, nil, lcBody(true, "12.345678"))
	require.NoError(t, err)
	require.NotNil(t, lcSetValue(t, store))
	assert.InDelta(t, 12.345678, *lcSetValue(t, store), 1e-12)

	store, err = lcCall(t, granted, lgStored(true, "auto_approve", apgCap(1)), lcMicros(5_000_000), nil, nil, lcBody(true, "null"))
	require.NoError(t, err)
	assert.Nil(t, lcSetValue(t, store), "null must clear, not write zero")
}

func TestLadderSweepCeiling_InvalidValuesRefused(t *testing.T) {
	t.Parallel()
	granted := apgCaller{perms: apgAdminPlus}
	for _, v := range []string{"0", "-1", "0.0000001", "1e13", `"5"`, "1.1234567"} {
		t.Run(v, func(t *testing.T) {
			t.Parallel()
			store, err := lcCall(t, granted, lgStored(true, "auto_approve", apgCap(1)), nil, nil, nil, lcBody(true, v))
			require.Error(t, err)
			ce, ok := IsClientError(err)
			require.True(t, ok)
			assert.Equal(t, 400, ce.code)
			assert.False(t, apgCalled(store, "UpsertLadderConfig"))
			assert.False(t, apgCalled(store, "SetLadderSweepCeiling"))
		})
	}
}

func TestLadderSweepCeiling_UnmigratedSchema(t *testing.T) {
	t.Parallel()
	granted := apgCaller{perms: apgAdminPlus}
	stored := lgStored(true, "auto_approve", apgCap(1))

	t.Run("stored row: refused with 503 before any write", func(t *testing.T) {
		t.Parallel()
		store, err := lcCall(t, granted, stored, nil, config.ErrLadderCeilingUnavailable, nil, lcBody(true, "10"))
		ce, ok := IsClientError(err)
		require.True(t, ok, "%v", err)
		assert.Equal(t, 503, ce.code)
		assert.False(t, apgCalled(store, "UpsertLadderConfig"))
	})
	t.Run("new row: 503 on the ceiling write", func(t *testing.T) {
		t.Parallel()
		_, err := lcCall(t, granted, nil, nil, nil, config.ErrLadderCeilingUnavailable, lcBody(true, "10"))
		ce, ok := IsClientError(err)
		require.True(t, ok, "%v", err)
		assert.Equal(t, 503, ce.code)
	})
	t.Run("key omitted: config writes keep working", func(t *testing.T) {
		t.Parallel()
		store, err := lcCall(t, granted, stored, nil, config.ErrLadderCeilingUnavailable, config.ErrLadderCeilingUnavailable, lcBody(true, ""))
		require.NoError(t, err)
		assert.False(t, apgCalled(store, "SetLadderSweepCeiling"))
	})
	t.Run("other read error fails closed", func(t *testing.T) {
		t.Parallel()
		store, err := lcCall(t, granted, stored, nil, fmt.Errorf("db down"), nil, lcBody(true, "10"))
		require.Error(t, err)
		_, isClient := IsClientError(err)
		assert.False(t, isClient)
		assert.False(t, apgCalled(store, "UpsertLadderConfig"))
	})
}

// A caller outside the account scope never reaches the ceiling write.
func TestLadderSweepCeiling_OutOfScopeAccount(t *testing.T) {
	t.Parallel()
	store := new(MockConfigStore)
	t.Cleanup(func() { store.AssertExpectations(t) })
	store.GetCloudAccountFn = func(_ context.Context, _ string) (*config.CloudAccount, error) { return nil, nil }
	h, req := apgCaller{perms: apgAdminPlus}.handlerAndReq(store, lcBody(true, "10"))
	_, err := h.upsertLadderConfig(context.Background(), req)
	require.Error(t, err)
	assert.False(t, apgCalled(store, "SetLadderSweepCeiling"))
	assert.False(t, apgCalled(store, "UpsertLadderConfig"))
}

func TestLadderSweepCeiling_GetListFillsCeiling(t *testing.T) {
	t.Parallel()
	run := func(t *testing.T, getErr error) ([]config.LadderConfigDB, error) {
		t.Helper()
		store := new(MockConfigStore)
		store.On("GetLadderConfigs", mock.Anything).Return([]config.LadderConfigDB{{ID: "a"}, {ID: "b"}}, nil)
		store.On("GetLadderSweepCeiling", mock.Anything, "a").Return(lcMicros(2_500_000), getErr)
		store.On("GetLadderSweepCeiling", mock.Anything, "b").Return((*int64)(nil), getErr).Maybe()
		h, req := apgCaller{perms: apgAdminOnly}.handlerAndReq(store, "")
		out, err := h.getLadderConfigs(context.Background(), req)
		if err != nil {
			return nil, err
		}
		return out.(map[string]any)["configs"].([]config.LadderConfigDB), nil
	}

	cfgs, err := run(t, nil)
	require.NoError(t, err)
	require.NotNil(t, cfgs[0].MaxHourlyCommitPerSweep)
	assert.Equal(t, 2.5, *cfgs[0].MaxHourlyCommitPerSweep)
	assert.Nil(t, cfgs[1].MaxHourlyCommitPerSweep, "NULL stays omitted, never 0")

	cfgs, err = run(t, config.ErrLadderCeilingUnavailable)
	require.NoError(t, err)
	assert.Nil(t, cfgs[0].MaxHourlyCommitPerSweep)

	_, err = run(t, fmt.Errorf("db down"))
	require.Error(t, err)
}

// The PUT response reflects the stored ceiling, also when the key was omitted.
func TestLadderSweepCeiling_ResponseReflectsStoredCeiling(t *testing.T) {
	t.Parallel()
	admin := apgCaller{perms: apgAdminOnly}
	call := func(body string, stored *int64) *config.LadderConfigDB {
		store := new(MockConfigStore)
		store.On("GetLadderConfig", mock.Anything, lgAcct, "aws").Return(lgStored(true, "auto_approve", apgCap(1)), nil)
		store.On("GetLadderSweepCeiling", mock.Anything, "cfg-1").Return(stored, nil).Maybe()
		store.On("UpsertLadderConfig", mock.Anything, mock.Anything).Return(&config.LadderConfigDB{ID: "cfg-1"}, nil)
		store.On("SetLadderSweepCeiling", mock.Anything, "cfg-1", mock.Anything).Return(nil).Maybe()
		store.GetCloudAccountFn = func(_ context.Context, id string) (*config.CloudAccount, error) {
			return &config.CloudAccount{ID: id, Provider: "aws"}, nil
		}
		h, req := admin.handlerAndReq(store, body)
		out, err := h.upsertLadderConfig(context.Background(), req)
		require.NoError(t, err)
		return out.(*config.LadderConfigDB)
	}
	got := call(lcBody(true, ""), lcMicros(5_000_000))
	require.NotNil(t, got.MaxHourlyCommitPerSweep, "omitted key: the stored ceiling is returned")
	assert.Equal(t, 5.0, *got.MaxHourlyCommitPerSweep)

	got = call(lcBody(true, "2"), lcMicros(5_000_000))
	require.NotNil(t, got.MaxHourlyCommitPerSweep, "sent value: the new ceiling is returned")
	assert.Equal(t, 2.0, *got.MaxHourlyCommitPerSweep)

	assert.Nil(t, call(lcBody(true, "null"), lcMicros(5_000_000)).MaxHourlyCommitPerSweep, "cleared")
	assert.Nil(t, call(lcBody(true, ""), nil).MaxHourlyCommitPerSweep, "none stays omitted")
}
