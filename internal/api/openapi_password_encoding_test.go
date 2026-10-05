package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Issue #402: every request password the handlers run through
// decodeBase64Password must be documented as base64-encoded in the spec.
func TestOpenAPIPasswordFieldsDocumentBase64(t *testing.T) {
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Description string `yaml:"description"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	require.NoError(t, yaml.Unmarshal(openapiSpec, &spec))

	for schema, field := range map[string]string{
		"LoginRequest":         "password",
		"SetupAdminRequest":    "password",
		"PasswordResetConfirm": "new_password",
	} {
		props, ok := spec.Components.Schemas[schema]
		require.True(t, ok, "schema %s missing", schema)
		prop, ok := props.Properties[field]
		require.True(t, ok, "%s.%s missing", schema, field)
		assert.Contains(t, prop.Description, "Base64-encoded", "%s.%s", schema, field)
	}
}
