package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/database"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/secrets"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeMigrate struct {
	versions    []uint // returned by successive readVersion calls
	dirty       bool
	runErr      error
	runCalls    int
	closed      bool
	envAtRun    string
	envWasSet   bool
	readCalls   int
	connectErr  error
	lockErr     error
	lockCalls   int
	unlockCalls int
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
		lockForce: func(context.Context, *pgxpool.Pool) (func(), error) {
			f.lockCalls++
			return func() { f.unlockCalls++ }, f.lockErr
		},
	}
}

func okResolver(t *testing.T) *mockSecretResolver {
	t.Helper()
	t.Setenv("ADMIN_PASSWORD_SECRET", "admin-secret")
	return &mockSecretResolver{getResult: "pw"}
}

func intPtr(n int) *int { return &n }

// migDirCfg returns a config whose migrations dir holds an up file per version.
func migDirCfg(t *testing.T, versions ...int) *database.Config {
	t.Helper()
	dir := t.TempDir()
	for _, v := range versions {
		name := fmt.Sprintf("%06d_m.up.sql", v)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("SELECT 1;"), 0o600))
	}
	return &database.Config{MigrationsPath: dir}
}

func seq(from, to int) []int {
	var out []int
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

func TestRunDeployMigrations_Success(t *testing.T) {
	t.Setenv(forceVersionEnv, "")
	f := &fakeMigrate{versions: []uint{3, 5}}
	rep, err := runDeployMigrations(context.Background(), &database.Config{}, okResolver(t), MigrateOptions{}, f.deps())
	require.NoError(t, err)
	assert.Equal(t, MigrateReport{From: 3, To: 5}, rep)
	assert.Equal(t, 1, f.runCalls)
	assert.True(t, f.closed)
}

func TestRunDeployMigrations_RunnerErrorPropagates(t *testing.T) {
	t.Setenv(forceVersionEnv, "")
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
	t.Setenv(forceVersionEnv, "")
	t.Setenv("ADMIN_PASSWORD_SECRET", "admin-secret")
	t.Setenv("ADMIN_EMAIL", "admin@example.com")
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
			_, err := runDeployMigrations(context.Background(), migDirCfg(t, seq(1, 9)...), okResolver(t),
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
			_, err := runDeployMigrations(context.Background(), migDirCfg(t, seq(1, 9)...), okResolver(t),
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

func TestRunDeployMigrations_StaleForceEnvWithoutFlagFailsLoud(t *testing.T) {
	t.Setenv(forceVersionEnv, "9")
	f := &fakeMigrate{versions: []uint{1}} // clean row: the env var alone must be refused
	_, err := runDeployMigrations(context.Background(), &database.Config{}, okResolver(t), MigrateOptions{}, f.deps())
	require.ErrorContains(t, err, "remove the env var or pass force_version")
	assert.Equal(t, 0, f.runCalls)
	assert.Equal(t, 0, f.readCalls)
	assert.False(t, f.closed, "must fail before connecting")
}

func TestRunDeployMigrations_EmptyForceEnvIsIgnored(t *testing.T) {
	t.Setenv(forceVersionEnv, "")
	f := &fakeMigrate{versions: []uint{1}}
	_, err := runDeployMigrations(context.Background(), &database.Config{}, okResolver(t), MigrateOptions{}, f.deps())
	require.NoError(t, err)
}

func TestRunDeployMigrations_NoAdminEmailSkipsAdminPassword(t *testing.T) {
	t.Setenv("ADMIN_EMAIL", "")
	t.Setenv("ADMIN_PASSWORD_SECRET", "")
	t.Setenv(forceVersionEnv, "")
	f := &fakeMigrate{versions: []uint{1}}
	_, err := runDeployMigrations(context.Background(), &database.Config{}, &mockSecretResolver{getErr: errors.New("denied")}, MigrateOptions{}, f.deps())
	require.NoError(t, err)
	assert.Equal(t, 1, f.runCalls)
}

func TestValidateForceVersion_NumberingGaps(t *testing.T) {
	cfg := migDirCfg(t, 1, 2, 58, 59, 60, 63, 64, 67, 70, 81, 83, 86, 134, 136)
	cases := []struct {
		dirty    int
		allowed  []int
		refused  []int
		lowerMsg string
	}{
		{63, []int{63, 60}, []int{62, 61, 59}, "60"},
		{70, []int{70, 67}, []int{69}, "67"},
		{83, []int{83, 81}, []int{82}, "81"},
		{86, []int{86, 83}, []int{85}, "83"},
		{136, []int{136, 134}, []int{135}, "134"},
		{64, []int{64, 63}, []int{62}, "63"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.dirty), func(t *testing.T) {
			for _, n := range tc.allowed {
				require.NoError(t, validateForceVersion(n, uint(tc.dirty), true, cfg.MigrationsPath), "force %d", n)
			}
			for _, n := range tc.refused {
				err := validateForceVersion(n, uint(tc.dirty), true, cfg.MigrationsPath)
				require.ErrorContains(t, err, "refusing force_version", "force %d", n)
				assert.ErrorContains(t, err, fmt.Sprintf("only %d or %s are allowed", tc.dirty, tc.lowerMsg))
			}
		})
	}
}

func TestValidateForceVersion_RefusesStrandedRow(t *testing.T) {
	t.Run("first migration dirty has no lower file", func(t *testing.T) {
		cfg := migDirCfg(t, 1, 2, 3)
		for _, n := range []int{0, 1} {
			err := validateForceVersion(n, 1, true, cfg.MigrationsPath)
			require.ErrorContains(t, err, "refusing force_version", "force %d", n)
			assert.ErrorContains(t, err, "first migration failed and needs manual SQL")
		}
	})
	t.Run("dirty version has no file in the image", func(t *testing.T) {
		cfg := migDirCfg(t, 1, 2, 3)
		for _, n := range []int{2, 4, 5} {
			err := validateForceVersion(n, 4, true, cfg.MigrationsPath)
			require.ErrorContains(t, err, "refusing force_version", "force %d", n)
			assert.ErrorContains(t, err, "has no migration file")
		}
	})
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
	t.Run("exactly the minimum budget is allowed", func(t *testing.T) {
		prev := timeUntil
		timeUntil = func(time.Time) time.Duration { return minMigrateBudget + migrateDeadlineMargin }
		t.Cleanup(func() { timeUntil = prev })
		got, err := migrateBudget(withDeadline(time.Hour), time.Hour)
		require.NoError(t, err)
		assert.Equal(t, minMigrateBudget, got)
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

// Both version reads must receive the budget context: the real reader cancels
// its advisory-lock wait when that context ends. The seam stands in for a read
// blocked on the lock and gives up only if it is handed an unbounded context.
func TestRunDeployMigrations_VersionReadHonoursBudget(t *testing.T) {
	for _, blockOn := range []int{1, 2} {
		t.Run(fmt.Sprintf("read%d", blockOn), func(t *testing.T) {
			t.Setenv(forceVersionEnv, "")
			f := &fakeMigrate{versions: []uint{3, 5}}
			deps := f.deps()
			fakeRead := deps.readVersion
			calls := 0
			deps.readVersion = func(ctx context.Context, p *pgxpool.Pool, path string) (uint, bool, error) {
				calls++
				if calls != blockOn {
					return fakeRead(ctx, p, path)
				}
				select {
				case <-ctx.Done():
					return 0, false, ctx.Err()
				case <-time.After(5 * time.Second):
					return 0, false, nil
				}
			}
			start := time.Now()
			_, err := migrateOnPool(withMigrateBudget(t, 200*time.Millisecond), nil, &database.Config{}, okResolver(t), MigrateOptions{}, deps)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			assert.Less(t, time.Since(start), 2*time.Second)
			assert.Equal(t, blockOn-1, f.runCalls)
		})
	}
}

func withMigrateBudget(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func TestRunDeployMigrations_ForceRunHoldsLockAcrossValidationAndRun(t *testing.T) {
	f := &fakeMigrate{versions: []uint{5, 6}, dirty: true}
	deps := f.deps()
	read := deps.readVersion
	deps.readVersion = func(ctx context.Context, p *pgxpool.Pool, path string) (uint, bool, error) {
		assert.Equal(t, 1, f.lockCalls, "lock must be taken before the version read")
		return read(ctx, p, path)
	}
	run := deps.run
	deps.run = func(ctx context.Context, p *pgxpool.Pool, path, e, pw string) error {
		assert.Equal(t, 0, f.unlockCalls, "lock must still be held during Force and Up")
		return run(ctx, p, path, e, pw)
	}
	_, err := runDeployMigrations(context.Background(), migDirCfg(t, 1, 2, 3, 4, 5, 6, 7, 8, 9), okResolver(t),
		MigrateOptions{ForceVersion: intPtr(5)}, deps)
	require.NoError(t, err)
	assert.Equal(t, 1, f.unlockCalls)
}

func TestRunDeployMigrations_ForceLockFailureStopsBeforeRun(t *testing.T) {
	f := &fakeMigrate{versions: []uint{5}, dirty: true, lockErr: errors.New("lock down")}
	_, err := runDeployMigrations(context.Background(), migDirCfg(t, 1, 2, 3, 4, 5, 6, 7, 8, 9), okResolver(t),
		MigrateOptions{ForceVersion: intPtr(5)}, f.deps())
	require.ErrorContains(t, err, "lock down")
	assert.Equal(t, 0, f.readCalls)
	assert.Equal(t, 0, f.runCalls)
}

func TestRunDeployMigrations_NoLockWithoutForce(t *testing.T) {
	f := &fakeMigrate{versions: []uint{5}}
	_, err := runDeployMigrations(context.Background(), migDirCfg(t, 1, 2, 3, 4, 5, 6, 7, 8, 9), okResolver(t),
		MigrateOptions{}, f.deps())
	require.NoError(t, err)
	assert.Equal(t, 0, f.lockCalls)
}
