package purchase

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// Fixed refusal notes (platform#682). Both are stored verbatim and are on the
// stored-error allowlist (errnotes.go), so neither may carry provider text.
const (
	gcpDuplicateCheckFailedNote = "GCP duplicate check could not list existing commitments; no purchase was submitted"
	gcpRecentCommitmentNote     = "a GCP commitment for this project, region and machine family started in the last 24 hours; no purchase was submitted, to avoid a duplicate"
)

// gcpCommitmentNameTokenLen mirrors computeengine idempotentNameTokenLen
// (providers/gcp/services/computeengine/client.go:1411).
const gcpCommitmentNameTokenLen = 32

// gcpCommitmentName mirrors computeengine idempotentCommitmentName
// (client.go:1423-1434) for a lowercase hex token: "cud-" + token[:32]. The
// parity test drives the real client, so a rule change there fails it.
func gcpCommitmentName(token string) string {
	if len(token) > gcpCommitmentNameTokenLen {
		token = token[:gcpCommitmentNameTokenLen]
	}
	return "cud-" + token
}

func isGCPCompute(providerName string, service common.ServiceType) bool {
	return common.ProviderType(providerName) == common.ProviderGCP && service == common.ServiceCompute
}

// gcpOwnCommitmentNames returns the commitment names this execution's selected
// GCP compute recs will create, built from the same per-rec tokens the purchase
// path passes to the provider. A listed commitment with one of these names was
// created by this lineage (a lost-response re-drive, or an earlier rec of the
// same run), so it is not a duplicate of the rec being bought.
func gcpOwnCommitmentNames(exec *config.PurchaseExecution, selected []int) map[string]bool {
	names := make(map[string]bool)
	for _, i := range selected {
		rec := exec.Recommendations[i]
		service, _ := mapServiceSlug(rec.Service)
		if !isGCPCompute(rec.Provider, service) {
			continue
		}
		names[gcpCommitmentName(common.DeriveIdempotencyToken(idempotencyLineageKey(exec), i))] = true
	}
	return names
}

// recentCommitmentFilter is the optional GCP filter recfilter asserts on the
// client it lists through (pkg/recfilter/dedupe.go:16-18, :71).
type recentCommitmentFilter interface {
	FilterRecommendationsForRecentCommitments(recs []common.Recommendation, existing []common.Commitment) (passed, filtered []common.Recommendation, err error)
}

// gcpRecentDedupeClient lists without this execution's own commitments and
// forwards the GCP filter. Embedding the interface does not promote
// FilterRecommendationsForRecentCommitments, and without it recfilter would
// fall back to its generic map path, which does not understand CUDs.
type gcpRecentDedupeClient struct {
	provider.ServiceClient
	own map[string]bool
}

func (c gcpRecentDedupeClient) GetExistingCommitments(ctx context.Context) ([]common.Commitment, error) {
	all, err := c.ServiceClient.GetExistingCommitments(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(slices.Clone(all), func(cm common.Commitment) bool { return c.own[cm.CommitmentID] }), nil
}

// FilterRecommendationsForRecentCommitments stamps the project onto the recs
// from the listed commitments (each carries Account = the client's project) and
// delegates. The platform does not know the project in ADC mode, so the rec
// cannot carry it itself.
func (c gcpRecentDedupeClient) FilterRecommendationsForRecentCommitments(recs []common.Recommendation, existing []common.Commitment) (passed, filtered []common.Recommendation, err error) {
	inner, ok := c.ServiceClient.(recentCommitmentFilter)
	if !ok {
		return nil, nil, errors.New("GCP service client has no recent-commitment filter")
	}
	if len(existing) == 0 {
		return nil, nil, errors.New("no recent commitments to filter against")
	}
	project := existing[0].Account
	for i := range existing {
		if existing[i].Account != project {
			return nil, nil, fmt.Errorf("recent commitments span projects %q and %q", project, existing[i].Account)
		}
	}
	stamped := slices.Clone(recs)
	for i := range stamped {
		stamped[i].Account = project
	}
	return inner.FilterRecommendationsForRecentCommitments(stamped, existing)
}

// gcpRecentCommitmentRefusal refuses a GCP compute rec when the project already
// has a commitment of the rec's region and machine family that started in the
// last 24 hours, and fails closed when the commitments cannot be listed. It
// returns nil for every other rec. A refusal is a beforeSubmission error: the
// purchase call is never made.
func gcpRecentCommitmentRefusal(ctx context.Context, client provider.ServiceClient, rec common.Recommendation, ownNames map[string]bool) error {
	if !isGCPCompute(string(rec.Provider), rec.Service) {
		return nil
	}
	rec.CommitmentType = common.CommitmentCUD
	passed, filtered, err := recfilter.NewDuplicateChecker(0).AdjustRecommendationsForExisting(
		ctx, []common.Recommendation{rec}, gcpRecentDedupeClient{ServiceClient: client, own: ownNames})
	switch {
	case err != nil:
		logging.Errorf("GCP duplicate check for %s in %s failed: %v", rec.ResourceType, rec.Region, err)
		return beforeSubmission(errors.New(gcpDuplicateCheckFailedNote))
	case len(filtered) > 0:
		return beforeSubmission(errors.New(gcpRecentCommitmentNote))
	case len(passed) != 1:
		logging.Errorf("GCP duplicate check for %s in %s returned %d passed recs, want 1", rec.ResourceType, rec.Region, len(passed))
		return beforeSubmission(errors.New(gcpDuplicateCheckFailedNote))
	}
	return nil
}
