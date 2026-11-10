package server

import (
	"context"
	"errors"
	"math"
	"os"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/secrets"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeMigrate struct {
	versions   []uint // returned by successive readVersion calls
	dirty      bool
	runErr     error
	runCalls   int
	closed     bool
	envAtRun   string
	envWasSet  bool
	readCalls  int
	connectErr error
}

func (f *fakeMigrate) deps() migrateDeps {
	return migrateDeps{
		connect: func(context.Context, *database.Config, secrets.Resolver) (*pgxpool.Pool, func(), error) {
			return nil, func() { f.closed = true }, f.connectErr
		},
		readVersion: func(context.Context, *pgxpool.Pool, string) (uint, bool, error) {
			v := f.versions[min(f.readCalls, len(f.versions)-1)]
			f.readCalls++
			return v, f.dirty && f.readCalls == 1, nil
		},
		run: func(context.Context, *pgxpool.Pool, string, string, string) error {
			f.runCalls++
			f.envAtRun, f.envWasSet = os.LookupEnv(forceVersionEnv)
			return f.runErr
		},
	}
}

func okResolver(t *testing.T) *mockSecretResolver {
	t.Helper()
	t.Setenv("ADMIN_PASSWORD_SECRET", "admin-secret")
	return &mockSecretResolver{getResult: "pw"}
}

func intPtr(n int) *int { return &n }

func TestRunDeployMigrations_Success(t *testing.T) {
	f := &fakeMigrate{versions: []uint{3, 5}}
	rep, err := runDeployMigrations(context.Background(), &database.Config{}, okResolver(t), MigrateOptions{}, f.deps())
	require.NoError(t, err)
	assert.Equal(t, MigrateReport{From: 3, To: 5}, rep)
	assert.Equal(t, 1, f.runCalls)
	assert.True(t, f.closed)
}

func TestRunDeployMigrations_RunnerErrorPropagates(t *testing.T) {
	f := &fakeMigrate{versions: []uint{3}, runErr: errors.New("boom")}
	_, err := runDeployMigrations(context.Background(), &database.Config{}, okResolver(t), MigrateOptions{}, f.deps())
	require.ErrorContains(t, err, "boom")
	assert.True(t, f.closed)
}

func TestRunDeployMigrations_NilConfigFailsLoud(t *testing.T) {
	f := &fakeMigrate{versions: []uint{0}}
	_, err := runDeployMigrations(context.Background(), nil, okResolver(t), MigrateOptions{}, f.deps())
	require.ErrorContains(t, err, "not configured")
	assert.Equal(t, 0, f.runCalls)
}

func TestRunDeployMigrations_AdminSecretFailureIsFatalAndClosesPool(t *testing.T) {
	t.Setenv("ADMIN_PASSWORD_SECRET", "admin-secret")
	f := &fakeMigrate{versions: []uint{0}}
	_, err := runDeployMigrations(context.Background(), &database.Config{},
		&mockSecretResolver{getErr: errors.New("denied")}, MigrateOptions{}, f.deps())
	require.ErrorContains(t, err, "admin password")
	assert.Equal(t, 0, f.runCalls)
	assert.True(t, f.closed)
}

func TestRunDeployMigrations_ForceGuardRefusals(t *testing.T) {
	cases := []struct {
		name    string
		dirty   bool
		version uint
		force   int
	}{
		{"not dirty", false, 5, 5},
		{"not dirty forward jump", false, 5, 9},
		{"dirty forward jump", true, 5, 6},
		{"dirty too far back", true, 5, 3},
		{"negative", true, 5, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeMigrate{versions: []uint{tc.version}, dirty: tc.dirty}
			_, err := runDeployMigrations(context.Background(), &database.Config{}, okResolver(t),
				MigrateOptions{ForceVersion: intPtr(tc.force)}, f.deps())
			require.ErrorContains(t, err, "refusing force_version")
			assert.Equal(t, 0, f.runCalls)
		})
	}
}

func TestRunDeployMigrations_ForceAcceptedAndEnvRestored(t *testing.T) {
	for _, force := range []int{5, 4} {
		for _, prior := range []*string{nil, ptr("77")} {
			os.Unsetenv(forceVersionEnv)
			if prior != nil {
				t.Setenv(forceVersionEnv, *prior)
			}
			f := &fakeMigrate{versions: []uint{5, 6}, dirty: true}
			_, err := runDeployMigrations(context.Background(), &database.Config{}, okResolver(t),
				MigrateOptions{ForceVersion: intPtr(force)}, f.deps())
			require.NoError(t, err)
			assert.True(t, f.envWasSet)
			assert.Equal(t, map[int]string{5: "5", 4: "4"}[force], f.envAtRun)

			got, set := os.LookupEnv(forceVersionEnv)
			if prior == nil {
				assert.False(t, set, "env must be unset again")
			} else {
				assert.True(t, set)
				assert.Equal(t, *prior, got)
			}
		}
	}
	os.Unsetenv(forceVersionEnv)
}

func TestRunDeployMigrations_NoForceLeavesEnvAlone(t *testing.T) {
	t.Setenv(forceVersionEnv, "9")
	f := &fakeMigrate{versions: []uint{1}}
	_, err := runDeployMigrations(context.Background(), &database.Config{}, okResolver(t), MigrateOptions{}, f.deps())
	require.NoError(t, err)
	assert.Equal(t, "9", f.envAtRun)
}

func ptr(s string) *string { return &s }

func TestMigrateBudget(t *testing.T) {
	withDeadline := func(d time.Duration) context.Context {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		t.Cleanup(cancel)
		return ctx
	}
	t.Run("no deadline uses requested", func(t *testing.T) {
		got, err := migrateBudget(context.Background(), 7*time.Minute)
		require.NoError(t, err)
		assert.Equal(t, 7*time.Minute, got)
	})
	t.Run("zero requested uses default", func(t *testing.T) {
		got, err := migrateBudget(context.Background(), 0)
		require.NoError(t, err)
		assert.Equal(t, defaultMigrateTimeout, got)
	})
	t.Run("ctx deadline minus margin wins", func(t *testing.T) {
		got, err := migrateBudget(withDeadline(5*time.Minute), 15*time.Minute)
		require.NoError(t, err)
		assert.InDelta(t, (5*time.Minute - migrateDeadlineMargin).Seconds(), got.Seconds(), 2)
	})
	t.Run("requested wins when shorter", func(t *testing.T) {
		got, err := migrateBudget(withDeadline(5*time.Minute), time.Minute)
		require.NoError(t, err)
		assert.Equal(t, time.Minute, got)
	})
	t.Run("under 30s left is an error", func(t *testing.T) {
		_, err := migrateBudget(withDeadline(45*time.Second), time.Minute)
		require.ErrorContains(t, err, "left before the caller deadline")
	})
}

func TestVersionToInt_Bounds(t *testing.T) {
	for _, v := range []uint{0, 1, math.MaxInt32} {
		got, err := versionToInt(v)
		require.NoError(t, err)
		assert.Equal(t, int(v), got)
	}
	_, err := versionToInt(math.MaxInt32 + 1)
	require.ErrorContains(t, err, "out of range")
}
