package iacfiles

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const reservedInstanceARN = "arn:aws:ec2:*:*:reserved-instances/*"

type awsPolicyStatement struct {
	Effect   string   `json:"Effect"`
	Action   []string `json:"Action"`
	Resource string   `json:"Resource"`
}

// permissionsPolicy extracts the PERMISSIONS_POLICY heredoc from a rendered-
// independent copy of an AWS CLI onboarding template (the heredoc is quoted and
// holds no template placeholders).
func permissionsPolicy(t *testing.T, path string) []awsPolicyStatement {
	t.Helper()
	raw, err := Templates.ReadFile(path)
	require.NoError(t, err)
	_, rest, found := strings.Cut(string(raw), "PERMISSIONS_POLICY=$(cat <<'JSON'\n")
	require.True(t, found, "PERMISSIONS_POLICY heredoc not found in %s", path)
	body, _, found := strings.Cut(rest, "\nJSON\n")
	require.True(t, found, "PERMISSIONS_POLICY heredoc terminator not found in %s", path)
	var doc struct {
		Statement []awsPolicyStatement `json:"Statement"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &doc))
	return doc.Statement
}

// TestAWSCLITemplatesTagReservedInstancesOnly pins #702: the member-account
// role can tag the reserved instance it just bought (the purchase idempotency
// tag), but only on reserved-instances resources, never on every EC2 resource.
func TestAWSCLITemplatesTagReservedInstancesOnly(t *testing.T) {
	for _, path := range []string{
		"templates/aws-cross-account-cli.sh.tmpl",
		"templates/aws-wif-cli.sh.tmpl",
	} {
		t.Run(path, func(t *testing.T) {
			var scoped, wildcardRedshiftTags int
			for _, stmt := range permissionsPolicy(t, path) {
				for _, action := range stmt.Action {
					switch action {
					case "ec2:CreateTags":
						assert.Equal(t, reservedInstanceARN, stmt.Resource,
							"ec2:CreateTags must be scoped to reserved instances")
						scoped++
					case "redshift:CreateTags", "redshift:DescribeTags":
						assert.Equal(t, "*", stmt.Resource,
							"redshift tag actions have no reserved-node resource type, so they are granted on *")
						wildcardRedshiftTags++
					}
				}
			}
			assert.Equal(t, 1, scoped, "ec2:CreateTags must appear exactly once")
			assert.Equal(t, 2, wildcardRedshiftTags, "redshift:CreateTags and redshift:DescribeTags must each appear once")
		})
	}
}
