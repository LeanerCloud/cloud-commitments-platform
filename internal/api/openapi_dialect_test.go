package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Issue #1075: the spec declares OpenAPI 3.0.3, where nullable needs an
// explicit type and exclusiveMinimum is a boolean paired with minimum.
func TestOpenAPIDialect303Schemas(t *testing.T) {
	type property struct {
		Type             string `yaml:"type"`
		Nullable         bool   `yaml:"nullable"`
		Minimum          *int   `yaml:"minimum"`
		ExclusiveMinimum any    `yaml:"exclusiveMinimum"`
	}
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]property `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	require.NoError(t, yaml.Unmarshal(openapiSpec, &spec))

	partial := spec.Components.Schemas["RecommendationsFreshness"].Properties["last_collection_partial"]
	assert.True(t, partial.Nullable)
	assert.Equal(t, "object", partial.Type, "nullable requires an explicit type in 3.0.3")

	stepCap := spec.Components.Schemas["PlanRequest"].Properties["max_step_commitment"]
	require.NotNil(t, stepCap.Minimum)
	assert.Equal(t, 0, *stepCap.Minimum)
	assert.Equal(t, true, stepCap.ExclusiveMinimum, "exclusiveMinimum must be boolean in 3.0.3")
}
