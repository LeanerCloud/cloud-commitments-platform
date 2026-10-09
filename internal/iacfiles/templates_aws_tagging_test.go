package iacfiles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const reservedInstanceARN = "arn:aws:ec2:*:*:reserved-instances/*"

type awsPolicyStatement struct {
	Effect   string   `json:"Effect"`
	Action   []string `json:"Action"`
	Resource string   `json:"Resource"`
}

// permissionsPolicy extracts the PERMISSIONS_POLICY heredoc from an AWS CLI
// onboarding template (the heredoc is quoted and holds no template
// placeholders).
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

// cfnStatements returns every mapping that has both an Action list and a
// Resource scalar, which is how a CloudFormation IAM statement looks.
func cfnStatements(t *testing.T, raw []byte) []awsPolicyStatement {
	t.Helper()
	var root yaml.Node
	require.NoError(t, yaml.Unmarshal(raw, &root))
	var out []awsPolicyStatement
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode {
			var stmt awsPolicyStatement
			var hasAction, hasResource bool
			for i := 0; i+1 < len(n.Content); i += 2 {
				key, val := n.Content[i].Value, n.Content[i+1]
				switch {
				case key == "Action" && val.Kind == yaml.SequenceNode:
					hasAction = true
					for _, item := range val.Content {
						stmt.Action = append(stmt.Action, item.Value)
					}
				case key == "Resource" && val.Kind == yaml.ScalarNode:
					hasResource = true
					stmt.Resource = val.Value
				}
			}
			if hasAction && hasResource {
				out = append(out, stmt)
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(&root)
	return out
}

var (
	tfActionList = regexp.MustCompile(`(?s)Action\s*=\s*\[(.*?)\]`)
	tfResource   = regexp.MustCompile(`Resource\s*=\s*"([^"]*)"`)
	quoted       = regexp.MustCompile(`"([^"]+)"`)
)

// tfStatements returns the statement objects of a Terraform jsonencode policy:
// the brace-balanced object around every `Sid =`.
func tfStatements(t *testing.T, raw []byte) []awsPolicyStatement {
	t.Helper()
	text := string(raw)
	sids := regexp.MustCompile(`Sid\s*=`).FindAllStringIndex(text, -1)
	out := make([]awsPolicyStatement, 0, len(sids))
	for _, loc := range sids {
		start, depth := loc[0], 0
		for ; start > 0; start-- {
			if text[start] == '}' {
				depth++
			}
			if text[start] == '{' {
				if depth == 0 {
					break
				}
				depth--
			}
		}
		end, depth := loc[1], 0
		for ; end < len(text); end++ {
			if text[end] == '{' {
				depth++
			}
			if text[end] == '}' {
				if depth == 0 {
					break
				}
				depth--
			}
		}
		chunk := text[start:end]
		var stmt awsPolicyStatement
		if m := tfActionList.FindStringSubmatch(chunk); m != nil {
			for _, q := range quoted.FindAllStringSubmatch(m[1], -1) {
				stmt.Action = append(stmt.Action, q[1])
			}
		}
		if m := tfResource.FindStringSubmatch(chunk); m != nil {
			stmt.Resource = m[1]
		}
		out = append(out, stmt)
	}
	return out
}

// memberAccountPolicies lists the seven member-account purchase policies:
// the two CLI onboarding templates, the four federation templates and the
// legacy CUDly-CrossAccount stack.
func memberAccountPolicies(t *testing.T) map[string][]awsPolicyStatement {
	t.Helper()
	root := filepath.Join("..", "..")
	read := func(rel string) []byte {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		require.NoError(t, err, rel)
		return raw
	}
	return map[string][]awsPolicyStatement{
		"templates/aws-cross-account-cli.sh.tmpl": permissionsPolicy(t, "templates/aws-cross-account-cli.sh.tmpl"),
		"templates/aws-wif-cli.sh.tmpl":           permissionsPolicy(t, "templates/aws-wif-cli.sh.tmpl"),

		"iac/federation/aws-cross-account/cloudformation/template.yaml": cfnStatements(t, read("iac/federation/aws-cross-account/cloudformation/template.yaml")),
		"iac/federation/aws-target/cloudformation/template.yaml":        cfnStatements(t, read("iac/federation/aws-target/cloudformation/template.yaml")),
		"cloudformation/stacks/CUDly-CrossAccount/template.yaml":        cfnStatements(t, read("cloudformation/stacks/CUDly-CrossAccount/template.yaml")),

		"iac/federation/aws-cross-account/terraform/main.tf": tfStatements(t, read("iac/federation/aws-cross-account/terraform/main.tf")),
		"iac/federation/aws-target/terraform/main.tf":        tfStatements(t, read("iac/federation/aws-target/terraform/main.tf")),
	}
}

// TestMemberAccountPoliciesTagReservedInstancesOnly pins #702 for every
// member-account policy: the role can tag the reserved instance it just bought
// (the purchase idempotency tag), but only on reserved-instances resources,
// never on every EC2 resource. redshift:CreateTags is deliberately absent: it
// cannot be scoped below "*" and whether Redshift can tag reserved nodes at all
// is unverified, so that half of #702 is still open.
func TestMemberAccountPoliciesTagReservedInstancesOnly(t *testing.T) {
	for path, statements := range memberAccountPolicies(t) {
		t.Run(path, func(t *testing.T) {
			require.NotEmpty(t, statements, "no policy statements parsed")
			var createTags int
			for _, stmt := range statements {
				for _, action := range stmt.Action {
					switch action {
					case "ec2:CreateTags":
						assert.Equal(t, reservedInstanceARN, stmt.Resource,
							"ec2:CreateTags must be scoped to reserved instances")
						createTags++
					case "redshift:CreateTags":
						t.Errorf("redshift:CreateTags must not be granted until it is verified and condition-scoped (#702)")
					}
				}
			}
			assert.Equal(t, 1, createTags, "ec2:CreateTags must appear exactly once")
		})
	}
}
