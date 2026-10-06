//go:build integration

package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegration_UpdateUserAdminFieldsReturnsStoredUpdatedAt(t *testing.T) {
	store := NewPostgresStore(setupAuthTestDB(t))
	f := newCredentialRaceFixture(t, store, "admin-fields-updated-at@example.com")
	read := f.stored()

	updated := *read
	updated.Active = false
	require.NoError(t, store.UpdateUserAdminFields(t.Context(), &updated, read.Email, read.GroupIDs, read.Active))

	stored := f.stored()
	assert.True(t, updated.UpdatedAt.After(read.UpdatedAt), "the caller's copy kept the stale updated_at")
	assert.True(t, updated.UpdatedAt.Equal(stored.UpdatedAt), "returned %v, stored %v", updated.UpdatedAt, stored.UpdatedAt)
}
