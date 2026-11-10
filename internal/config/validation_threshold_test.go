package config

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateRIExchangeUtilizationThreshold(t *testing.T) {
	for _, v := range []float64{0.1, 50, 95, 100} {
		assert.NoError(t, ValidateRIExchangeUtilizationThreshold("t", v), "%v", v)
	}
	for _, v := range []float64{0, -1, 100.0001, math.NaN(), math.Inf(1)} {
		err := ValidateRIExchangeUtilizationThreshold("utilization_threshold", v)
		if assert.Error(t, err, "%v", v) {
			assert.Contains(t, err.Error(), "utilization_threshold must be greater than 0 and at most 100")
		}
	}
}
