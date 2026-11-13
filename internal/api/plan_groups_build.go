package api

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// boundGroupMinCount is the rollback sentinel on a bound entry: every
// recommendation's count is at least 1, so it filters nothing, while binaries
// that predate bound plans refuse any min_count (issue #1746).
const boundGroupMinCount = 1

// boundGroupDefaults mirror the flat request's create defaults.
const (
	defaultGroupTerm    = 3
	defaultGroupPayment = "no-upfront"
)

// groupTermPayment returns the group's term and its payment normalized for the
// group's provider, applying the create defaults for an omitted field. An
// unrecognized payment is a 400.
func groupTermPayment(i int, g *PlanGroupRequest) (term int, payment string, err error) {
	term = defaultGroupTerm
	if g.Term != nil && *g.Term != 0 {
		term = *g.Term
	}
	payment = defaultGroupPayment
	if g.Payment != nil && *g.Payment != "" {
		payment = *g.Payment
	}
	normalized, ok := config.NormalizePaymentOption(g.Provider, payment)
	if !ok {
		return 0, "", NewClientError(400, fmt.Sprintf("groups[%d].payment %q is not valid for provider %q", i, payment, g.Provider))
	}
	return term, normalized, nil
}

// buildBoundServices builds the services map of a bound plan: one entry per
// group, keyed "provider/service#<group_id>", bound to the identities of the
// group's recommendations (recs[i] belongs to groups[i]). It checks nothing
// about the recommendations; resolveGroupRecs has already matched them to
// their group. Group ids are fresh, so a retried create never collides.
func buildBoundServices(groups []PlanGroupRequest, recs [][]config.RecommendationRecord) (map[string]config.ServiceConfig, error) {
	if len(groups) != len(recs) {
		return nil, fmt.Errorf("buildBoundServices: %d groups but %d recommendation sets", len(groups), len(recs))
	}
	services := make(map[string]config.ServiceConfig, len(groups))
	for i := range groups {
		g := &groups[i]
		term, payment, err := groupTermPayment(i, g)
		if err != nil {
			return nil, err
		}
		identities := make([]string, 0, len(recs[i]))
		for j := range recs[i] {
			key, err := config.RecIdentityKey(&recs[i][j])
			if err != nil {
				return nil, fmt.Errorf("recommendation %s: %w", recs[i][j].ID, err)
			}
			identities = append(identities, key)
		}
		coverage := float64(config.DefaultCoveragePercent)
		if g.Coverage != nil {
			coverage = float64(*g.Coverage)
		}
		gid := uuid.NewString()
		services[g.Provider+"/"+g.Service+config.BoundKeySeparator+gid] = config.ServiceConfig{
			Provider:          g.Provider,
			Service:           g.Service,
			Enabled:           true,
			Term:              term,
			Payment:           payment,
			Coverage:          coverage,
			MinCount:          boundGroupMinCount,
			GroupID:           gid,
			BoundIdentities:   identities,
			MaxStepCommitment: g.MaxStepCommitment,
		}
	}
	return services, nil
}
