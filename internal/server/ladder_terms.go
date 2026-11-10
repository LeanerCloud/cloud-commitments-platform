package server

import (
	"fmt"

	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// ladderTerms is a commitment term and payment option pair.
type ladderTerms struct {
	term    pkgladder.Term
	payment pkgladder.PaymentOption
}

// ladderPlanTerms is the only pair ladder tranches are sized, stored and
// bought at (owner decision D6=A), whatever the global default says.
var ladderPlanTerms = ladderTerms{term: config.LadderTerm, payment: config.LadderPayment}

// ladderGlobalTerms parses the global default pair. It fails loud: a corrupt
// default must not reach a money-path decision.
func ladderGlobalTerms(g *config.GlobalConfig) (ladderTerms, error) {
	term, err := ladderTermFromYears(g.DefaultTerm)
	if err != nil {
		return ladderTerms{}, fmt.Errorf("ladder_run: invalid default_term in global config: %w", err)
	}
	payment, err := pkgladder.ParsePaymentOption(g.DefaultPayment)
	if err != nil {
		return ladderTerms{}, fmt.Errorf("ladder_run: invalid default_payment in global config: %w", err)
	}
	return ladderTerms{term: term, payment: payment}, nil
}
