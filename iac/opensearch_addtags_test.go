package iac

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Regression test for #42: the OpenSearch client calls opensearch:AddTags after
// a purchase (providers/aws/services/opensearch/client.go), which authorizes
// against es:AddTags. Every template that grants the other OpenSearch purchase
// actions must grant it too, or the tagging fails with AccessDenied.
func TestOpenSearchStatementsGrantAddTags(t *testing.T) {
	files := []string{
		"federation/aws-cross-account/cloudformation/template.yaml",
		"federation/aws-cross-account/terraform/main.tf",
		"federation/aws-target/cloudformation/template.yaml",
		"federation/aws-target/terraform/main.tf",
		"../cloudformation/stacks/CUDly-CrossAccount/template.yaml",
	}
	purchase := regexp.MustCompile(`es:PurchaseReservedInstanceOffering`)
	for _, rel := range files {
		t.Run(filepath.Base(filepath.Dir(rel)), func(t *testing.T) {
			data, err := os.ReadFile(rel)
			require.NoError(t, err)
			content := string(data)
			loc := purchase.FindAllStringIndex(content, -1)
			require.Len(t, loc, 1, "%s: expected exactly one OpenSearch purchase statement", rel)
			end := strings.Index(content[loc[0][1]:], "Resource")
			require.GreaterOrEqual(t, end, 0, "%s: no Resource after the OpenSearch actions", rel)
			stmt := content[loc[0][0] : loc[0][1]+end]
			require.Contains(t, stmt, "es:AddTags", "%s: OpenSearch statement lacks es:AddTags", rel)
		})
	}
}
