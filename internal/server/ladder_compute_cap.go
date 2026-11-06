package server

import (
	"errors"

	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// ladderComputeCapRefusal fails closed for an uncapped Compute SP ladder: its
// rate is an account-wide blend, and only max_hourly_commit_per_run bounds that bias.
func ladderComputeCapRefusal(dbCfg *config.LadderConfigDB, layers []pkgladder.LayerSpec) error {
	if dbCfg.MaxHourlyCommitPerRun != nil {
		return nil
	}
	for _, l := range layers {
		if l.Type == pkgladder.LayerComputeSP {
			return errors.New("max_hourly_commit_per_run is required for a ladder config that sizes the Compute Savings Plan layer: its rate is an account-wide blend, so an uncapped run is refused")
		}
	}
	return nil
}
