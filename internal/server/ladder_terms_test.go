package server

import (
	"testing"

	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// ladderTestGlobal is the global default pair the executeLadderRun tests pass.
var ladderTestGlobal = ladderTerms{term: pkgladder.Term1Year, payment: pkgladder.PaymentNoUpfront}

func TestLadderGlobalTerms(t *testing.T) {
	t.Parallel()
	tests := []struct {
		want    ladderTerms
		name    string
		payment string
		errText string
		years   int
	}{
		{name: "3y all-upfront", years: 3, payment: "all-upfront", want: ladderTerms{pkgladder.Term3Year, pkgladder.PaymentAllUpfront}, errText: ""},
		{name: "1y no-upfront", years: 1, payment: "no-upfront", want: ladderTerms{pkgladder.Term1Year, pkgladder.PaymentNoUpfront}, errText: ""},
		{name: "1y partial-upfront", years: 1, payment: "partial-upfront", want: ladderTerms{pkgladder.Term1Year, pkgladder.PaymentPartialUpfront}, errText: ""},
		{name: "3y no-upfront", years: 3, payment: "no-upfront", want: ladderTerms{pkgladder.Term3Year, pkgladder.PaymentNoUpfront}, errText: ""},
		{name: "term 2", years: 2, payment: "no-upfront", want: ladderTerms{}, errText: "invalid default_term"},
		{name: "term 0", years: 0, payment: "no-upfront", want: ladderTerms{}, errText: "invalid default_term"},
		{name: "payment monthly", years: 3, payment: "monthly", want: ladderTerms{}, errText: "invalid default_payment"},
		{name: "payment empty", years: 3, payment: "", want: ladderTerms{}, errText: "invalid default_payment"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ladderGlobalTerms(&config.GlobalConfig{DefaultTerm: tt.years, DefaultPayment: tt.payment})
			if tt.errText != "" {
				require.ErrorContains(t, err, tt.errText)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
