package purchase

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

const (
	spMinHourly     = 0.001
	spMaxHourly     = 1000000.0
	spMaxDecimalPls = 5
)

// spRawRangeError refuses the raw amount, before truncation, the way the AWS
// provider's spcommitment.Normalize does (that package is internal to the
// provider module, so the rule is mirrored here).
func spRawRangeError(amount float64) error {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 {
		return fmt.Errorf("hourly commitment must be a positive finite value, got %g", amount)
	}
	if amount < spMinHourly || amount > spMaxHourly {
		return fmt.Errorf("hourly commitment must be between %g and %g, got %g", spMinHourly, spMaxHourly, amount)
	}
	return nil
}

// truncateDecimals cuts the shortest decimal string of x to places digits.
// Multiplying a binary float can cross a boundary (0.29*1e5 floors to 28999),
// so the cut is made on the decimal text, never rounding up.
func truncateDecimals(x float64, places int) (float64, error) {
	whole, fraction, hasFraction := strings.Cut(strconv.FormatFloat(x, 'f', -1, 64), ".")
	if !hasFraction {
		return x, nil
	}
	if len(fraction) > places {
		fraction = fraction[:places]
	}
	out, err := strconv.ParseFloat(whole+"."+fraction, 64)
	if err != nil {
		return 0, fmt.Errorf("parse truncated hourly commitment: %w", err)
	}
	return out, nil
}

// floorSPHourly returns the Savings Plan hourly commitment the provider will
// actually submit: range checked on the raw amount, then truncated to five
// decimals, mirroring providers/aws/internal/spcommitment/commitment.go in
// cloud-commitments-go.
func floorSPHourly(amount float64) (float64, error) {
	if err := spRawRangeError(amount); err != nil {
		return 0, err
	}
	return truncateDecimals(amount, spMaxDecimalPls)
}

// ladderTermYears maps the tranche term to whole years. The provider client
// also accepts "1" and "3", but the tranche contract does not.
func ladderTermYears(t ladder.Term) (int, error) {
	switch t {
	case ladder.Term1Year:
		return 1, nil
	case ladder.Term3Year:
		return 3, nil
	}
	return 0, fmt.Errorf("unsupported ladder term %q", t)
}

// spMonthlyCost is the recurring monthly cost of a no-upfront plan, mirroring
// spRecurringMonthlyCost in cloud-commitments-go providers/aws/recommendations
// so the record's total commitment is never zero.
func spMonthlyCost(hourly float64, years int) float64 {
	return (hourly * common.HoursPerYear * float64(years)) / (12 * float64(years))
}

// requireNoUpfront refuses every payment option but no-upfront: no verified
// upfront formula exists, so a partial or all-upfront record would be
// mis-stated to the caps and notices.
func requireNoUpfront(p ladder.PaymentOption) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p != ladder.PaymentNoUpfront {
		return fmt.Errorf("payment option %q is unsupported in v1, only %q", p, ladder.PaymentNoUpfront)
	}
	return nil
}
