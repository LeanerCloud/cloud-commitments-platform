package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// The per-sweep ceiling lives outside the four ladder_configs queries (see
// LadderConfigDB.MaxHourlyCommitPerSweep), so the config API reads and writes
// it through these helpers. Writes that raise or clear it on an armed config
// need the auto-purchase grant (ladderConfigArmEscalates).

// ladderCeilingUnavailableError maps the unmigrated-schema error to a 503 and
// passes any other error through.
func ladderCeilingUnavailableError(err error) error {
	if !errors.Is(err, config.ErrLadderCeilingUnavailable) {
		return err
	}
	return NewClientErrorWithDetails(503,
		"max_hourly_commit_per_sweep is unavailable until the database migration that adds it is applied",
		map[string]any{})
}

func ladderCeilingUSD(micros *int64) *float64 {
	if micros == nil {
		return nil
	}
	v := float64(*micros) / 1e6
	return &v
}

// loadLadderCeilingOnList fills the ceiling of each config. An unmigrated
// schema leaves every ceiling unset (nil), which is also how the sweep reads
// it; any other error fails the request.
func (h *Handler) loadLadderCeilingOnList(ctx context.Context, configs []config.LadderConfigDB) error {
	for i := range configs {
		micros, err := h.config.GetLadderSweepCeiling(ctx, configs[i].ID)
		if errors.Is(err, config.ErrLadderCeilingUnavailable) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to read ladder sweep ceiling: %w", err)
		}
		configs[i].MaxHourlyCommitPerSweep = ladderCeilingUSD(micros)
	}
	return nil
}

// fillLadderCeiling sets the stored ceiling on a row the API returns; an
// unmigrated schema leaves it unset.
func (h *Handler) fillLadderCeiling(ctx context.Context, cfg *config.LadderConfigDB) error {
	micros, err := h.config.GetLadderSweepCeiling(ctx, cfg.ID)
	if errors.Is(err, config.ErrLadderCeilingUnavailable) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read ladder sweep ceiling: %w", err)
	}
	cfg.MaxHourlyCommitPerSweep = ladderCeilingUSD(micros)
	return nil
}

// loadStoredLadderCeiling sets before.MaxHourlyCommitPerSweep for the arming
// gate. A failure refuses the write (fail closed), 503 when unmigrated.
func (h *Handler) loadStoredLadderCeiling(ctx context.Context, before *config.LadderConfigDB) error {
	if before == nil {
		return nil
	}
	micros, err := h.config.GetLadderSweepCeiling(ctx, before.ID)
	if err != nil {
		return ladderCeilingUnavailableError(fmt.Errorf("failed to load ladder sweep ceiling: %w", err))
	}
	before.MaxHourlyCommitPerSweep = ladderCeilingUSD(micros)
	return nil
}

// saveLadderCeiling persists the requested ceiling after the row upsert. A
// null that clears nothing is skipped so it works before the migration.
func (h *Handler) saveLadderCeiling(ctx context.Context, saved *config.LadderConfigDB, want, stored *float64) error {
	if want == nil && stored == nil {
		return nil
	}
	if err := h.config.SetLadderSweepCeiling(ctx, saved.ID, want); err != nil {
		return ladderCeilingUnavailableError(fmt.Errorf("ladder config saved but its sweep ceiling was not: %w", err))
	}
	saved.MaxHourlyCommitPerSweep = want
	return nil
}
