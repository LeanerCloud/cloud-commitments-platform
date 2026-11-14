package api

import (
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestResolveRIExchangeDirect_RouteIsAuthUserPOST(t *testing.T) {
	t.Parallel()
	r := NewRouter(&Handler{})
	for _, route := range r.routes {
		if route.PathPrefix == "/api/ri-exchange/direct-executions/" && route.PathSuffix == "/resolve" {
			assert.Equal(t, "POST", route.Method)
			assert.Equal(t, AuthUser, route.Auth, "the outer middleware enforces the session and CSRF")
			return
		}
	}
	t.Fatal("POST /api/ri-exchange/direct-executions/{id}/resolve is not registered")
}

// The settle age rests on a bounded window: the direct execute takes one
// deadline before saving the row, the terminal save gets its own timeout, and
// a margin covers clock skew. A later timeout bump must not silently open the
// window.
func TestResolveRIExchangeDirect_SettleAgeCoversTheExecutionWindow(t *testing.T) {
	t.Parallel()
	window := riExchangeExecutionTimeout + riExchangeTerminalSaveTimeout + time.Minute
	assert.Greater(t, config.RIExchangeDirectResolveSubmittedAfter, window)
}
