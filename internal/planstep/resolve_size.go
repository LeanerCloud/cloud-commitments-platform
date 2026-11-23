package planstep

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// size returns shown sized to n units (count recs) or to product/10000 of its
// hourly commitment (Savings Plans; product = coverage*percentPerStep,
// multiplied before dividing) and priced from the unsuppressed stored row.
// keep=false means the rec sized to nothing.
func size(shown, stored *config.RecommendationRecord, product float64, n int, share *big.Rat) (config.RecommendationRecord, bool, error) {
	if stored.Count <= 0 {
		return config.RecommendationRecord{}, false, fmt.Errorf("stored count %d cannot price a share", stored.Count)
	}
	if err := checkStoredSPCount(stored); err != nil {
		return config.RecommendationRecord{}, false, err
	}
	out := *shown
	out.SuppressedCount, out.SuppressionExpiresAt, out.PrimarySuppressionExecutionID = 0, nil, nil
	out.Selected = true
	out.RecommendedCount = shown.Count
	var ratio float64
	var err error
	if common.IsSavingsPlan(common.ServiceType(stored.Service)) {
		ratio, err = sizeSP(&out, stored, product, share)
	} else {
		ratio = sizeCount(&out, stored, n)
	}
	if err != nil || ratio == 0 {
		return out, false, err
	}
	out.UpfrontCost = stored.UpfrontCost * ratio
	out.Savings = stored.Savings * ratio
	out.MonthlyCost = scaled(stored.MonthlyCost, ratio)
	out.OnDemandCost = scaled(stored.OnDemandCost, ratio)
	// The costs are stored's, so their currency is too (issue #361).
	out.CommitmentCurrency = stored.CommitmentCurrency
	if out.UpfrontCost < 0 || (out.MonthlyCost != nil && *out.MonthlyCost < 0) ||
		out.UpfrontCost+monthlyTotal(&out) <= 0 {
		return out, false, errors.New("stored row carries no usable price")
	}
	return out, true, nil
}

// sizeCount sets out.Count to n and returns the ratio to stored.Count; 0
// means it sized to nothing.
func sizeCount(out, stored *config.RecommendationRecord, n int) float64 {
	if n <= 0 {
		return 0
	}
	out.Count = n
	return float64(n) / float64(stored.Count)
}

// sizeSP rewrites out's hourly_commitment (rounded down to whole cents, never
// up) and returns the ratio to the stored commitment; 0 means below one cent.
func sizeSP(out, stored *config.RecommendationRecord, product float64, share *big.Rat) (float64, error) {
	keys, hourly, err := decodeHourly(stored.Details)
	if err != nil {
		return 0, err
	}
	sized := math.Floor(hourly*product/100) / 100
	if share != nil {
		sized, err = exactSPHourly(hourly, share)
		if err != nil {
			return 0, err
		}
	}
	return applySPSize(out, stored, keys, hourly, sized)
}

// SizeSavingsPlan sizes a Savings Plans recommendation to share (an exact
// fraction of the stored hourly commitment) the way a plan step does: the
// hourly commitment is floored to whole cents, never rounded up. It rewrites
// out.Details (every other key untouched) and out.Count, and returns the
// ratio sized/stored; 0 means the share is below the one-cent minimum and out
// is left unchanged. The caller owns scaling the cost fields by the ratio.
func SizeSavingsPlan(out, stored *config.RecommendationRecord, share *big.Rat) (float64, error) {
	if err := checkStoredSPCount(stored); err != nil {
		return 0, err
	}
	keys, hourly, err := decodeHourly(stored.Details)
	if err != nil {
		return 0, err
	}
	sized, err := exactSPHourly(hourly, share)
	if err != nil {
		return 0, err
	}
	return applySPSize(out, stored, keys, hourly, sized)
}

func applySPSize(out, stored *config.RecommendationRecord, keys map[string]json.RawMessage, hourly, sized float64) (float64, error) {
	if sized < spHourlyStep {
		return 0, nil
	}
	details, err := encodeHourly(keys, sized)
	if err != nil {
		return 0, err
	}
	out.Details = details
	out.Count = stored.Count
	return sized / hourly, nil
}

func monthlyTotal(r *config.RecommendationRecord) float64 {
	if r.MonthlyCost == nil {
		return 0
	}
	return *r.MonthlyCost * float64(r.Term) * 12
}

func scaled(v *float64, ratio float64) *float64 {
	if v == nil {
		return nil
	}
	s := *v * ratio
	return &s
}

// decodeHourly splits Details into raw keys and its hourly_commitment so the
// rewrite can put every other key (platform, tenancy, ...) back untouched.
func decodeHourly(raw json.RawMessage) (keys map[string]json.RawMessage, hourly float64, err error) {
	if err = json.Unmarshal(raw, &keys); err != nil || keys == nil {
		return nil, 0, fmt.Errorf("savings plan details unreadable: %w", errors.Join(err, errors.New("not a JSON object")))
	}
	if err = json.Unmarshal(keys["hourly_commitment"], &hourly); err != nil || hourly <= 0 {
		return nil, 0, errors.New("savings plan details carry no positive hourly_commitment")
	}
	return keys, hourly, nil
}

func encodeHourly(keys map[string]json.RawMessage, hourly float64) (json.RawMessage, error) {
	v, err := json.Marshal(hourly)
	if err != nil {
		return nil, err
	}
	keys["hourly_commitment"] = v
	return json.Marshal(keys)
}
