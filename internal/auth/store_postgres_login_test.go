package auth

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestPostgresStore_LoginBookkeepingErrors(t *testing.T) {
	for _, success := range []bool{false, true} {
		for _, databaseError := range []bool{false, true} {
			db := new(MockDBConnection)
			store := NewPostgresStore(db)
			var execErr error
			if databaseError {
				execErr = assert.AnError
			}
			db.On("Exec", mock.Anything, mock.AnythingOfType("string"), mock.Anything).
				Return(pgconn.NewCommandTag("UPDATE 0"), execErr).Once()
			operation := store.RecordFailedLogin
			if success {
				operation = store.RecordSuccessfulLogin
			}
			err := operation(context.Background(), "missing-user")
			if databaseError {
				require.ErrorIs(t, err, assert.AnError)
			} else {
				require.EqualError(t, err, "user not found: missing-user")
			}
			db.AssertExpectations(t)
		}
	}
}
