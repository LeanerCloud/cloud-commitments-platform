package auth

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPermissionStrictScope(t *testing.T) {
	t.Parallel()
	for _, admin := range []bool{false, true} {
		permission := Permission{Action: ActionUpdate, Resource: ResourceConfig}
		if admin {
			permission.Action, permission.Resource = ActionAdmin, ResourceAll
		}
		for _, tc := range []struct {
			name string
			held *PermissionConstraints
			req  PermissionConstraints
			want bool
		}{
			{"unrestricted", nil, PermissionConstraints{StrictScope: true}, true},
			{"unknown-account", &PermissionConstraints{AccountIDs: []string{"a"}}, PermissionConstraints{StrictScope: true}, false},
			{"unknown-provider", &PermissionConstraints{Providers: []string{"aws"}}, PermissionConstraints{StrictScope: true}, false},
			{"unknown-service", &PermissionConstraints{Services: []string{"ec2"}}, PermissionConstraints{StrictScope: true}, false},
			{"unknown-region", &PermissionConstraints{Regions: []string{"eastus"}}, PermissionConstraints{StrictScope: true}, false},
			{"known-normalized-region", &PermissionConstraints{Regions: []string{"EastUS "}}, PermissionConstraints{StrictScope: true, Regions: []string{" eastus"}}, true},
			{"mixed-regions", &PermissionConstraints{Regions: []string{"eastus"}}, PermissionConstraints{StrictScope: true, Regions: []string{"eastus", "westus"}}, false},
			{"known-scope", &PermissionConstraints{AccountIDs: []string{"a"}, Providers: []string{"aws"}}, PermissionConstraints{StrictScope: true, AccountIDs: []string{"a"}, Providers: []string{"aws"}}, true},
			{"legacy-absent-dimension", &PermissionConstraints{AccountIDs: []string{"a"}}, PermissionConstraints{}, true},
			{"amount-unaffected", &PermissionConstraints{MaxPurchaseAmount: 1}, PermissionConstraints{StrictScope: true}, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				permission.Constraints = tc.held
				allowed, err := PermissionsAllowForConstraintSets([]Permission{permission}, ActionUpdate, ResourceConfig, []PermissionConstraints{tc.req})
				require.NoError(t, err)
				assert.Equal(t, tc.want, allowed, "admin=%t", admin)
			})
		}
	}
	assert.False(t, permissionsAllow([]Permission{{Action: ActionAdmin, Resource: ResourceAll}}, ActionExecute, ResourceRIExchange, &PermissionConstraints{StrictScope: true}))
}

func TestStrictScopeIsRequestOnly(t *testing.T) {
	t.Parallel()
	original := PermissionConstraints{StrictScope: true, Providers: []string{"aws"}}
	encoded, err := json.Marshal(original)
	require.NoError(t, err)
	var decoded PermissionConstraints
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.False(t, decoded.StrictScope)
	require.NoError(t, json.Unmarshal([]byte(`{"StrictScope":true,"strict_scope":true}`), &decoded))
	assert.False(t, decoded.StrictScope)
	field, found := reflect.TypeFor[PermissionConstraints]().FieldByName("StrictScope")
	require.True(t, found)
	assert.Equal(t, "-", field.Tag.Get("dynamodbav"))
	assert.Equal(t, original.Providers, decoded.Providers)
}
