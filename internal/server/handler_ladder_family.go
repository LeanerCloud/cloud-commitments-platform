package server

import (
	"math/big"
	"time"

	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

// ladderAmountKey identifies one purchasable commitment line: a layer plus,
// for family-bound EC2 Instance SP allocations, the instance family. Two
// family allocations share a layer, so keying by layer alone would give both
// the summed immediate amount.
type ladderAmountKey struct {
	layer  pkgladder.LayerType
	family string
}

// immediateTrancheAmounts sums, per (layer, family), the tranches that fire at
// the run start (FireAfter <= now: the AfterDays==0 ramp step). It sums the
// stored (floored) amounts, so the display plan never shows more than the
// persisted rows, and skips the tranches buildTrancheDBRows drops; a key whose
// immediate tranches all floor to zero has no entry. It fails loud on a
// malformed or non-positive amount, like buildTrancheDBRows.
func immediateTrancheAmounts(tranches []pkgladder.Tranche, now time.Time) (map[ladderAmountKey]*big.Rat, error) {
	sums := make(map[ladderAmountKey]*big.Rat)
	for i := range tranches {
		tr := &tranches[i]
		if tr.FireAfter.After(now) {
			continue
		}
		r, stored, err := storedTrancheAmount(tr)
		if err != nil {
			return nil, err
		}
		if !stored {
			continue
		}
		k := ladderAmountKey{layer: tr.Layer, family: tr.Family}
		if sums[k] == nil {
			sums[k] = new(big.Rat)
		}
		sums[k].Add(sums[k], r)
	}
	return sums, nil
}
