//go:build integration
// +build integration

package config

// Real-DB test for registration uniqueness (issue #137). The constraint names
// the store matches on come from migration 000017, so they are checked against
// the errors Postgres actually raises rather than hand-built ones.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
)

func newPendingRegistration(token, externalID string) *AccountRegistration {
	return &AccountRegistration{
		ReferenceToken: token, Status: "pending", Provider: "aws", ExternalID: externalID,
		AccountName: "Prod", ContactEmail: "owner@example.com",
	}
}

func TestCreateAccountRegistration_UniquenessPostgres(t *testing.T) {
	ctx := context.Background()
	container := testhelpers.RequireMigratedDatabase(ctx, t, getTestMigrationsPath())
	store := NewPostgresStore(container.DB)

	first := newPendingRegistration("tok-137-a", "111111111111")
	require.NoError(t, store.CreateAccountRegistration(ctx, first))

	// Same account submitted twice while the first is pending.
	err := store.CreateAccountRegistration(ctx, newPendingRegistration("tok-137-b", "111111111111"))
	require.ErrorIs(t, err, ErrDuplicatePendingRegistration)
	assert.NotErrorIs(t, err, ErrReferenceTokenCollision)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, pgUniqueViolation, pgErr.Code)
	assert.Equal(t, registrationPendingUniqueIndex, pgErr.ConstraintName)

	// A different account reusing an existing token is a token collision,
	// not a duplicate registration.
	err = store.CreateAccountRegistration(ctx, newPendingRegistration("tok-137-a", "222222222222"))
	require.ErrorIs(t, err, ErrReferenceTokenCollision)
	assert.NotErrorIs(t, err, ErrDuplicatePendingRegistration)
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, registrationReferenceTokenConstraint, pgErr.ConstraintName)

	// A unique violation on any other constraint (here the primary key) is
	// neither sentinel.
	dupID := newPendingRegistration("tok-137-c", "333333333333")
	dupID.ID = first.ID
	err = store.CreateAccountRegistration(ctx, dupID)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrDuplicatePendingRegistration)
	assert.NotErrorIs(t, err, ErrReferenceTokenCollision)

	// Once the first registration leaves pending, the account may register again.
	_, err = container.DB.Pool().Exec(ctx,
		`UPDATE account_registrations SET status = 'rejected' WHERE id = $1`, first.ID)
	require.NoError(t, err)
	require.NoError(t, store.CreateAccountRegistration(ctx, newPendingRegistration("tok-137-d", "111111111111")))
}

// Approval keeps the encrypted credential only until it is saved to the
// account: after UpdateAccountRegistration with a cleared payload the column
// is NULL, not just blank in memory.
func TestUpdateAccountRegistration_ClearsCredentialPayloadPostgres(t *testing.T) {
	ctx := context.Background()
	container := testhelpers.RequireMigratedDatabase(ctx, t, getTestMigrationsPath())
	store := NewPostgresStore(container.DB)

	reg := newPendingRegistration("tok-cred-a", "444444444444")
	reg.RegCredentialType = "aws_access_keys"
	reg.RegCredentialPayload = "encrypted-payload"
	require.NoError(t, store.CreateAccountRegistration(ctx, reg))

	var stored *string
	query := `SELECT reg_credential_payload FROM account_registrations WHERE id = $1`
	require.NoError(t, container.DB.Pool().QueryRow(ctx, query, reg.ID).Scan(&stored))
	require.NotNil(t, stored, "the pending registration holds its payload")

	reg.Status = "approved"
	reg.RegCredentialPayload = ""
	require.NoError(t, store.UpdateAccountRegistration(ctx, reg))

	require.NoError(t, container.DB.Pool().QueryRow(ctx, query, reg.ID).Scan(&stored))
	assert.Nil(t, stored, "the approved registration must not keep the encrypted payload")
}

// A rejected registration will never be approved, so it must not keep the
// encrypted credential; a transition to any other status leaves it alone.
func TestTransitionRegistrationStatus_RejectClearsCredentialPayloadPostgres(t *testing.T) {
	ctx := context.Background()
	container := testhelpers.RequireMigratedDatabase(ctx, t, getTestMigrationsPath())
	store := NewPostgresStore(container.DB)
	query := `SELECT reg_credential_payload FROM account_registrations WHERE id = $1`

	for _, tc := range []struct {
		status    string
		wantStore bool
	}{{"rejected", false}, {"approved", true}} {
		reg := newPendingRegistration("tok-rej-"+tc.status, "55555555555"+map[string]string{"rejected": "1", "approved": "2"}[tc.status])
		reg.RegCredentialType = "aws_access_keys"
		reg.RegCredentialPayload = "encrypted-payload"
		require.NoError(t, store.CreateAccountRegistration(ctx, reg))

		reg.Status = tc.status
		require.NoError(t, store.TransitionRegistrationStatus(ctx, reg, "pending", nil))

		var stored *string
		require.NoError(t, container.DB.Pool().QueryRow(ctx, query, reg.ID).Scan(&stored))
		if tc.wantStore {
			assert.NotNil(t, stored, "%s keeps the payload for the approve flow to save", tc.status)
		} else {
			assert.Nil(t, stored, "%s must not keep the encrypted payload", tc.status)
		}
	}
}

// The not-found error of GetRIExchangeRecord is the sentinel the API maps to 404.
func TestGetRIExchangeRecord_MissingIsSentinelPostgres(t *testing.T) {
	ctx := context.Background()
	container := testhelpers.RequireMigratedDatabase(ctx, t, getTestMigrationsPath())
	store := NewPostgresStore(container.DB)

	_, err := store.GetRIExchangeRecord(ctx, "6f1c2a8e-0d4b-4c1e-9a3f-692000000009")
	require.ErrorIs(t, err, ErrRIExchangeRecordNotFound)
}
