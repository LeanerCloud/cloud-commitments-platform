package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeCollectionPartial(t *testing.T) {
	assert.Nil(t, decodeCollectionPartial(nil), "NULL means the last collection was complete")

	got := decodeCollectionPartial([]byte(`{"providers":[{"provider":"azure","incomplete_accounts":1,"attempted_scopes":3,"failed_scopes":2,"failed_details":0,"reasons":{"throttled":2}}]}`))
	require.NotNil(t, got)
	assert.Equal(t, []PartialProviderSweep{{
		Provider: "azure", IncompleteAccounts: 1, AttemptedScopes: 3, FailedScopes: 2,
		Reasons: map[string]int{PartialReasonThrottled: 2},
	}}, got.Providers)

	assert.Empty(t, got.Providers[0].HeldSince, "a row stored before the hold bound decodes without the new fields")
	assert.Empty(t, got.Providers[0].ExpiredHoldAccountIDs)

	withBound := decodeCollectionPartial([]byte(`{"providers":[{"provider":"azure","incomplete_accounts":0,"attempted_scopes":0,"failed_scopes":0,"failed_details":0,"reasons":{"pricing_hold_expired":1},"held_since":{"a":"2026-11-01T12:00:00Z"},"expired_hold_account_ids":["b"]}]}`))
	require.NotNil(t, withBound)
	assert.Equal(t, []string{"b"}, withBound.Providers[0].ExpiredHoldAccountIDs)
	assert.True(t, withBound.Providers[0].HeldSince["a"].Equal(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)))

	assert.Nil(t, decodeCollectionPartial([]byte(`not json`)),
		"an undecodable diagnostic must not fail the freshness read")
}
