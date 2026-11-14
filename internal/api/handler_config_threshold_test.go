package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A written threshold of 0 used to pass validation and be stored as 95 by the
// store, a different value than the caller sent. PUT /api/config now refuses it
// the way PUT /api/ri-exchange/config does.
func TestUpdateConfig_RefusesAWrittenThresholdOutsideItsRange(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"zero":                        `{"ri_exchange_utilization_threshold": 0}`,
		"mixed-case zero":             `{"Ri_Exchange_Utilization_Threshold": 0}`,
		"negative":                    `{"ri_exchange_utilization_threshold": -1}`,
		"above 100":                   `{"ri_exchange_utilization_threshold": 100.5}`,
		"zero hidden by a later null": `{"ri_exchange_utilization_threshold": 0, "ri_exchange_utilization_threshold": null}`,
	} {
		t.Run(name, func(t *testing.T) {
			stored := disarmedConfig()
			stored.RIExchangeUtilizationThreshold = 95
			r, store := autoGateSessionRouter(t, autoGateAdminPlusExchange, stored)

			_, err := r.Route(context.Background(), "PUT", genericPath, autoGateTokenReq(body))

			require.Error(t, err)
			ce, ok := IsClientError(err)
			require.True(t, ok, "expected a client error, got %v", err)
			assert.Equal(t, 400, ce.code)
			assert.Contains(t, err.Error(), "ri_exchange_utilization_threshold")
			assert.False(t, wrote(store), "a rejected write must not reach the store")
		})
	}
}

func TestUpdateConfig_AcceptsAValidWrittenThreshold(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{"ri_exchange_utilization_threshold": 0.1}`,
		`{"ri_exchange_utilization_threshold": 80}`,
		`{"ri_exchange_utilization_threshold": 100}`,
	} {
		t.Run(body, func(t *testing.T) {
			r, store := autoGateSessionRouter(t, autoGateAdminPlusExchange, disarmedConfig())

			_, err := r.Route(context.Background(), "PUT", genericPath, autoGateTokenReq(body))

			require.NoError(t, err)
			assert.True(t, wrote(store))
		})
	}
}

// Presence-based: a save that does not write the threshold must keep working
// on a row that still stores a legacy 0.
func TestUpdateConfig_StoredZeroThresholdDoesNotBlockUnrelatedWrites(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"partial save":   `{"default_term": 3}`,
		"null threshold": `{"ri_exchange_utilization_threshold": null}`,
	} {
		t.Run(name, func(t *testing.T) {
			stored := disarmedConfig()
			stored.RIExchangeUtilizationThreshold = 0
			r, store := autoGateSessionRouter(t, autoGateAdminPlusExchange, stored)

			_, err := r.Route(context.Background(), "PUT", genericPath, autoGateTokenReq(body))

			require.NoError(t, err)
			assert.True(t, wrote(store))
		})
	}
}
