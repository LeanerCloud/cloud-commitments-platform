package auth

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The auth doubles (testify mocks, the fixed test CSRF key) must stay out of
// the production binaries (issue #322). Test code reaches them through
// test_helpers_test.go and the authtest package, neither of which any
// non-test package may import.
func TestProductionBinariesLinkNoAuthTestDoubles(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "../../cmd/...").CombinedOutput()
	require.NoError(t, err, string(out))

	for _, pkg := range strings.Fields(string(out)) {
		require.NotContains(t, pkg, "github.com/stretchr/testify", "production binary links a test-only dependency")
		require.NotContains(t, pkg, "internal/auth/authtest", "production binary links the auth test doubles")
	}
}
