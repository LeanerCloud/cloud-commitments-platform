package api

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// A late finalize can move a resolved row to completed or failed, so the
// resolve response must document every provider status.
func TestOpenAPIDirectResolveStatusEnumMatchesGoStatuses(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "openapi.yaml"))
	require.NoError(t, err)
	type enumProp struct {
		Enum []string `yaml:"enum"`
	}
	var doc struct {
		Paths map[string]struct {
			Post struct {
				Responses map[string]struct {
					Content struct {
						JSON struct {
							Schema struct {
								Properties struct {
									Status enumProp `yaml:"status"`
								} `yaml:"properties"`
							} `yaml:"schema"`
						} `yaml:"application/json"`
					} `yaml:"content"`
				} `yaml:"responses"`
			} `yaml:"post"`
		} `yaml:"paths"`
	}
	require.NoError(t, yaml.Unmarshal(data, &doc))
	path, ok := doc.Paths["/api/ri-exchange/direct-executions/{id}/resolve"]
	require.True(t, ok, "resolve path is documented")
	require.ElementsMatch(t, []string{
		config.RIExchangeDirectStatusSubmitted,
		config.RIExchangeDirectStatusUnknown,
		config.RIExchangeDirectStatusCompleted,
		config.RIExchangeDirectStatusFailed,
	}, path.Post.Responses["200"].Content.JSON.Schema.Properties.Status.Enum)
}
