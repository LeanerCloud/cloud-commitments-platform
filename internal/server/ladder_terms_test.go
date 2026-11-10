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
	tests := []struct {
		name    string
		years   int
		payment string
		want    ladderTerms
		errText string
	}{
		{"3y all-upfront", 3, "all-upfront", ladderTerms{pkgladder.Term3Year, pkgladder.PaymentAllUpfront}, ""},
		{"1y no-upfront", 1, "no-upfront", ladderTerms{pkgladder.Term1Year, pkgladder.PaymentNoUpfront}, ""},
		{"1y partial-upfront", 1, "partial-upfront", ladderTerms{pkgladder.Term1Year, pkgladder.PaymentPartialUpfront}, ""},
		{"3y no-upfront", 3, "no-upfront", ladderTerms{pkgladder.Term3Year, pkgladder.PaymentNoUpfront}, ""},
		{"term 2", 2, "no-upfront", ladderTerms{}, "invalid default_term"},
		{"term 0", 0, "no-upfront", ladderTerms{}, "invalid default_term"},
		{"payment monthly", 3, "monthly", ladderTerms{}, "invalid default_payment"},
		{"payment empty", 3, "", ladderTerms{}, "invalid default_payment"},
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
