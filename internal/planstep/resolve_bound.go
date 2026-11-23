package planstep

import (
	"slices"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// boundServiceOf is planServiceOf for a bound plan: the enabled group whose
// bound identities hold key.
func boundServiceOf(plan *config.PurchasePlan, key string) (config.ServiceConfig, bool) {
	for _, k := range sortedKeys(plan.Services) {
		svc := plan.Services[k]
		if svc.Enabled && slices.Contains(svc.BoundIdentities, key) {
			return svc, true
		}
	}
	return config.ServiceConfig{}, false
}

// boundSet restricts a bound group to its bound identities and records which
// of them the strict read showed. A nil set (an unbound entry) admits every rec.
type boundSet struct {
	ids        []string
	want       map[string]bool // identity -> shown
	mismatched []string        // shown identities their group's term, payment or accounts rejected
}

func newBoundSet(ids []string) *boundSet {
	if len(ids) == 0 {
		return nil
	}
	b := &boundSet{ids: ids, want: make(map[string]bool, len(ids))}
	for _, id := range ids {
		b.want[id] = false
	}
	return b
}

// filter keeps the recs whose identity is bound and marks them shown.
func (b *boundSet) filter(recs []config.RecommendationRecord) ([]config.RecommendationRecord, error) {
	if b == nil {
		return recs, nil
	}
	var kept []config.RecommendationRecord
	for i := range recs {
		key, err := config.RecIdentityKey(&recs[i])
		if err != nil {
			return nil, err
		}
		if _, ok := b.want[key]; ok {
			b.want[key] = true
			kept = append(kept, recs[i])
		}
	}
	return kept, nil
}

// matching keeps the recs that have the plan service's term, payment and a
// target account. For a bound group it records each rejected rec's identity.
func (b *boundSet) matching(recs []config.RecommendationRecord, term int, wantPayment string, allowed map[string]struct{}) ([]config.RecommendationRecord, error) {
	var kept []config.RecommendationRecord
	for i := range recs {
		match, err := MatchesPlan(&recs[i], term, wantPayment, allowed)
		if err != nil {
			return nil, err
		}
		if match {
			kept = append(kept, recs[i])
			continue
		}
		if b == nil {
			continue
		}
		key, err := config.RecIdentityKey(&recs[i])
		if err != nil {
			return nil, err
		}
		b.mismatched = append(b.mismatched, key)
	}
	return kept, nil
}

func (b *boundSet) mismatchedIDs() []string {
	if b == nil {
		return nil
	}
	return b.mismatched
}

func (b *boundSet) missing() []string {
	if b == nil {
		return nil
	}
	var out []string
	for _, id := range b.ids {
		if !b.want[id] {
			out = append(out, id)
		}
	}
	return out
}
