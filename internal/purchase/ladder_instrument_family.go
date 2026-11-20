package purchase

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// checkTrancheFamily rejects a family the go ladder would reject
// (validateFamilyForLayer there is unexported, so the rule is mirrored): only
// an EC2 Instance SP is family bound, and the name must be copied verbatim
// into purchase requests, so it may hold no whitespace or control rune.
func checkTrancheFamily(tr *config.LadderTrancheDB) error {
	if tr.Family == "" {
		return nil
	}
	if tr.LayerType != ladder.LayerEC2InstanceSP {
		return fmt.Errorf("ladder tranche %s: family %q is only valid on layer %s, got layer %s",
			tr.ID, tr.Family, ladder.LayerEC2InstanceSP, tr.LayerType)
	}
	if strings.ContainsFunc(tr.Family, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return fmt.Errorf("ladder tranche %s: family %q must not contain whitespace or control characters", tr.ID, tr.Family)
	}
	return nil
}

// buildFamilyBoundEC2SPRecs buys the whole tranche in its own family as one
// record. Stored recs never choose or split the family here: looping over
// their families would buy the full amount once per family.
func buildFamilyBoundEC2SPRecs(in LadderBuildInput, years int, total float64) ([]config.RecommendationRecord, error) {
	rec, err := newEC2SPRec(in, years, in.Tranche.Family, total)
	if err != nil {
		return nil, fmt.Errorf("ladder tranche %s: %w", in.Tranche.ID, err)
	}
	return []config.RecommendationRecord{rec}, nil
}
