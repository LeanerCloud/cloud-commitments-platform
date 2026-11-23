package migrations

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/golang-migrate/migrate/v4"
)

// maybeForceMigrationVersion inspects CUDLY_FORCE_MIGRATION_VERSION and,
// when set to a non-negative integer, calls m.Force(N) which clears the
// dirty flag and pins the migration version. Used as a one-shot recovery
// path after a failed or interrupted migration leaves schema_migrations in a
// dirty state. The file itself rolled back as a whole (see
// TestMigrationsAreTransactional), so its effects are either all present or
// all absent; the operator decides which.
//
// Operator flow:
//  1. Check the current DB state (e.g. by inspecting actual schema via
//     logs or a one-off job).
//  2. If the dirty migration's SQL effects landed (e.g. the PK exists),
//     set CUDLY_FORCE_MIGRATION_VERSION=N where N is the dirty version —
//     this marks it clean and subsequent Up() picks up from the next
//     migration after N.
//  3. If the dirty migration's effects did NOT land, set
//     CUDLY_FORCE_MIGRATION_VERSION to the highest migration version below
//     N that has a file — subsequent Up() re-runs N. That is N-1 unless the
//     numbering has a gap (e.g. there is no 000061 or 000062): Up() fails
//     with "no migration found for version" when the forced version has no
//     file.
//  4. Redeploy / restart so the env var is visible.
//  5. After the first successful run, remove the env var.
//
// A non-numeric value is rejected with a loud error rather than silently
// ignored — forcing a wrong version is destructive, so typos should
// surface immediately.
func maybeForceMigrationVersion(m *migrate.Migrate) error {
	v := os.Getenv("CUDLY_FORCE_MIGRATION_VERSION")
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return fmt.Errorf("CUDLY_FORCE_MIGRATION_VERSION must be a non-negative integer, got %q", v)
	}
	logging.Infof("CUDLY_FORCE_MIGRATION_VERSION=%d set: forcing migration state (clears dirty flag)", n)
	if err := m.Force(n); err != nil {
		return fmt.Errorf("failed to force migration version to %d: %w", n, err)
	}
	logging.Infof("Forced migration state to version %d", n)
	return nil
}

// maybeRefuseDirty detects a dirty schema_migrations row and, when
// CUDLY_MIGRATION_DIRTY_CHECK is enabled (the default), returns a loud error with
// recovery instructions rather than silently clearing the dirty flag.
//
// WHY NOT AUTO-CLEAR: golang-migrate records version=N, dirty=true BEFORE
// running N's SQL, in a separate transaction. Every migration file then runs
// as one PostgreSQL transaction (the implicit transaction of a multi-statement
// query; TestMigrationsAreTransactional explains the mechanics and keeps
// non-transactional statements out). When a migration fails or is interrupted
// (Lambda timeout, connection drop, context cancellation), the SQL transaction is
// rolled back completely -- N's effects are ABSENT from the schema -- but the
// dirty flag persists. The previous Force(N)+Up() auto-heal treated such a
// rolled-back migration as fully applied and silently continued at N+1,
// permanently losing N's DDL while schema_migrations reported success (the
// 000074 divergence class: columns reported as present by the version record
// but absent from the actual schema, causing 42703 errors at runtime).
//
// The "genuine narrow race" (N's SQL committed successfully but the subsequent
// dirty=false update failed, e.g. due to a Lambda timeout between those two
// steps) also results in dirty=true at N, but in that case N's effects ARE
// present. Without per-migration effect probes the two scenarios are
// indistinguishable from schema_migrations alone, so we take the conservative
// path: fail loud so the migration-failed alarm fires and an operator can
// inspect the actual schema before choosing the correct recovery target.
//
// RECOVERY (via CUDLY_FORCE_MIGRATION_VERSION -- see maybeForceMigrationVersion):
//   - If migration N's SQL effects ARE confirmed present in the schema: set
//     CUDLY_FORCE_MIGRATION_VERSION=N. Force(N) clears the dirty flag and
//     the subsequent Up() continues with the next migration after N.
//   - If migration N's SQL effects are NOT present (rolled back): set
//     CUDLY_FORCE_MIGRATION_VERSION to the previous migration version that has
//     a file (N-1 unless the numbering has a gap; the error below always
//     prints N-1). Force resets state to it and the subsequent Up()
//     re-applies N then continues.
//
// FAIL-OPEN: the app still starts on this error. ensureDB (app.go) swallows
// migration failures and the broken schema surfaces via /health + the
// CloudWatch alarm, never via a crash-loop.
//
// Set CUDLY_MIGRATION_DIRTY_CHECK=false to bypass this check entirely; Up() then
// refuses to start and the dirty state surfaces as golang-migrate's ErrDirty,
// wrapped by RunMigrations as "failed to run migrations: Dirty database version
// N. Fix and force version.", without recovery guidance.
//
// CUDLY_FORCE_MIGRATION_VERSION takes precedence: it runs before this function
// and leaves the row clean, so this function is a no-op when both are set.
//
// The ParseBool gate is duplicated from internal/server.getEnvBool rather than
// imported because the migrations package must not depend on internal/server,
// which would invert the dependency direction.
func maybeRefuseDirty(m *migrate.Migrate) error {
	if !dirtyCheckEnabled() {
		return nil
	}

	version, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		// No migrations recorded yet -> nothing to heal.
		return nil
	}
	if err != nil {
		return fmt.Errorf("dirty check: failed to read migration version: %w", err)
	}
	if !dirty {
		return nil
	}

	// Cannot safely determine whether migration N's SQL effects landed:
	//   - Common case (interrupted/rolled back): effects ABSENT; clearing the
	//     dirty flag would silently record the never-applied migration as
	//     complete, hiding the missing DDL from schema_migrations forever.
	//   - Rare race (N committed, dirty=false update failed): effects PRESENT;
	//     clearing dirty would be safe -- but we cannot detect this case
	//     without per-migration effect probes.
	// Conservative choice: fail loud so the alarm fires and an operator
	// inspects the actual schema before choosing the recovery target.
	logging.Errorf("Database is DIRTY at version %d: dirty check cannot safely clear the dirty flag without "+
		"knowing whether migration %d's SQL effects were applied. Inspect the schema and redeploy "+
		"with CUDLY_FORCE_MIGRATION_VERSION to recover. Set CUDLY_MIGRATION_DIRTY_CHECK=false to suppress this check.",
		version, version)
	return fmt.Errorf(
		"dirty check: migration %d is dirty; cannot verify whether its SQL effects were applied -- "+
			"clearing the dirty flag risks recording a never-applied migration as complete "+
			"(silent schema divergence). Inspect the actual schema, then redeploy with: "+
			"CUDLY_FORCE_MIGRATION_VERSION=%d if migration %d's effects are confirmed present "+
			"(Force clears dirty, Up() continues at %d), or "+
			"CUDLY_FORCE_MIGRATION_VERSION=%d if the migration was NOT applied and must be re-run "+
			"(Force resets to %d, Up() re-applies %d); "+
			"set CUDLY_MIGRATION_DIRTY_CHECK=false to suppress this check and surface the raw dirty error",
		version, version, version, version+1, version-1, version-1, version)
}

// dirtyCheckEnabled reports whether the dirty check should run. DEFAULT-ON: it
// returns true unless CUDLY_MIGRATION_DIRTY_CHECK is explicitly set to a
// strconv.ParseBool falsey value (0/f/false/...). Unset, empty, or unparseable
// values keep dirty check enabled. Kept local to the migrations package to avoid
// an inverted dependency on internal/server (see maybeRefuseDirty).
func dirtyCheckEnabled() bool {
	v := os.Getenv("CUDLY_MIGRATION_DIRTY_CHECK")
	if v == "" {
		return true
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		// Unparseable value -> keep the safe default (enabled) rather than
		// silently disabling self-recovery on a typo.
		return true
	}
	return b
}
