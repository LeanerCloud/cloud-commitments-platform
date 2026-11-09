package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The list route is session-authenticated, never public, and read-only.
func TestListRIExchangeDirectExecutions_RouteIsAuthUserGET(t *testing.T) {
	r := NewRouter(&Handler{})
	for _, route := range r.routes {
		if route.ExactPath == "/api/ri-exchange/direct-executions" {
			assert.Equal(t, "GET", route.Method)
			assert.Equal(t, AuthUser, route.Auth)
			return
		}
	}
	t.Fatal("GET /api/ri-exchange/direct-executions is not registered")
}
