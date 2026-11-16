// Package api provides the HTTP API handlers for the CUDly dashboard.
package api

import (
	"context"
	"errors"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/commitmentopts"
)

// commitmentOptionsResponse is the JSON shape returned by
// GET /api/commitment-options. On success, AWS carries the probed combos
// keyed by service (rds, elasticache, ...). Azure carries the cached Azure
// probe rows in the same shape and is omitted when there are none. GCP is
// deliberately omitted: its commitment rules stay hardcoded in the frontend.
type commitmentOptionsResponse struct {
	AWS    map[string][]commitmentOptionCombo `json:"aws,omitempty"`
	Azure  map[string][]commitmentOptionCombo `json:"azure,omitempty"`
	Status string                             `json:"status"`
}

// commitmentOptionCombo is one (term, payment) tuple as the frontend
// consumes it. Dropping Provider/Service from the persisted Combo shape
// keeps the wire payload compact.
type commitmentOptionCombo struct {
	Payment string `json:"payment"`
	Term    int    `json:"term"`
}

// getCommitmentOptions returns the dynamically-probed AWS commitment
// options. On any error or when the probe hasn't run yet, it returns
// {"status":"unavailable"} (200, not a 5xx) so the frontend can fall
// back to its hardcoded defaults without tripping the generic
// error-toast path.
func (h *Handler) getCommitmentOptions(ctx context.Context) (*commitmentOptionsResponse, error) { //nolint:unparam // error return is part of router handler interface; always nil when returning unavailable status
	if h.commitmentOpts == nil {
		return &commitmentOptionsResponse{Status: "unavailable"}, nil
	}
	opts, err := h.commitmentOpts.Get(ctx)
	if err != nil && !errors.Is(err, commitmentopts.ErrNoData) {
		// A transient DB blip or context cancellation collapses to
		// "unavailable" so it doesn't break the Settings page overlay fetch.
		// ErrNoData (no AWS account / fresh install) is the quiet path and
		// must not hide cached Azure rows; anything else is logged so
		// operators can still trace DB/connection issues.
		logging.Warnf("commitmentopts handler: %v", err)
		return &commitmentOptionsResponse{Status: "unavailable"}, nil
	}

	// Empty AWS (ErrNoData, or a probe that returned nothing after
	// normalization) leaves the frontend on its hardcoded rules.
	awsOut := toOptionCombos(opts["aws"])
	azureOut := h.cachedAzureOptions(ctx)
	if len(awsOut) == 0 && len(azureOut) == 0 {
		return &commitmentOptionsResponse{Status: "unavailable"}, nil
	}
	return &commitmentOptionsResponse{Status: "ok", AWS: awsOut, Azure: azureOut}, nil
}

// cachedAzureOptions reads the stored Azure probe rows only (no probing on
// the request path). A read error is logged and drops just the Azure section.
func (h *Handler) cachedAzureOptions(ctx context.Context) map[string][]commitmentOptionCombo {
	azure, ok, err := h.commitmentOpts.CachedOptions(ctx, "azure")
	if err != nil {
		logging.Warnf("commitmentopts handler: azure options: %v", err)
		return nil
	}
	if !ok {
		return nil
	}
	return toOptionCombos(azure)
}

// toOptionCombos converts probed combos to the wire shape. It returns nil
// for an empty input so the key is omitted from the JSON.
func toOptionCombos(byService map[string][]commitmentopts.Combo) map[string][]commitmentOptionCombo {
	if len(byService) == 0 {
		return nil
	}
	out := make(map[string][]commitmentOptionCombo, len(byService))
	for svc, combos := range byService {
		out[svc] = make([]commitmentOptionCombo, 0, len(combos))
		for _, c := range combos {
			out[svc] = append(out[svc], commitmentOptionCombo{Term: c.TermYears, Payment: c.Payment})
		}
	}
	return out
}
