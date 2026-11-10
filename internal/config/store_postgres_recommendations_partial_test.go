package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeCollectionPartial(t *testing.T) {
	none, err := decodeCollectionPartial(nil)
	require.NoError(t, err)
	assert.Nil(t, none, "NULL means the last collection was complete")

	got, err := decodeCollectionPartial([]byte(`{"providers":[{"provider":"azure","incomplete_accounts":1,"attempted_scopes":3,"failed_scopes":2,"failed_details":0,"reasons":{"throttled":2}}]}`))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, []PartialProviderSweep{{
		Provider: "azure", IncompleteAccounts: 1, AttemptedScopes: 3, FailedScopes: 2,
		Reasons: map[string]int{PartialReasonThrottled: 2},
	}}, got.Providers)

	assert.Empty(t, got.Providers[0].HeldSince, "a row stored before the hold bound decodes without the new fields")
	assert.Empty(t, got.Providers[0].ExpiredHoldAccountIDs)

	withBound, err := decodeCollectionPartial([]byte(`{"providers":[{"provider":"azure","incomplete_accounts":0,"attempted_scopes":0,"failed_scopes":0,"failed_details":0,"reasons":{"pricing_hold_expired":1},"held_since":{"a":"2026-11-01T12:00:00Z"},"expired_hold_account_ids":["b"]}]}`))
	require.NoError(t, err)
	require.NotNil(t, withBound)
	assert.Equal(t, []string{"b"}, withBound.Providers[0].ExpiredHoldAccountIDs)
	assert.True(t, withBound.Providers[0].HeldSince["a"].Equal(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)))
}

// JSONB cannot hold invalid text, so a corrupt column is a valid JSON value of
// the wrong shape. It must be an error: reading it as "no marker" would let the
// purchase gate pass an Azure hold it can no longer see.
func TestDecodeCollectionPartial_WrongShapeIsAnError(t *testing.T) {
	for name, raw := range map[string]string{
		"array":                 `[]`,
		"number":                `7`,
		"string":                `"held"`,
		"not json":              `not json`,
		"wrong-typed providers": `{"providers":"azure"}`,
		"wrong-typed provider":  `{"providers":[{"provider":1}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := decodeCollectionPartial([]byte(raw))
			require.Error(t, err)
			assert.Nil(t, got)
		})
	}
}

func TestDecodeCollectionPartial_EmptyValues(t *testing.T) {
	empty, err := decodeCollectionPartial([]byte{})
	require.NoError(t, err)
	assert.Nil(t, empty)
	for _, raw := range []string{`null`, `{}`} {
		got, err := decodeCollectionPartial([]byte(raw))
		require.NoError(t, err, raw)
		require.NotNil(t, got, raw)
		assert.Empty(t, got.Providers)
	}
}
