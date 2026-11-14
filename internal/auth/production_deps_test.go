package auth

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The auth doubles (testify mocks, the fixed test CSRF key) must stay out of
// the production binaries (issue #322). Test code reaches them through
// test_helpers_test.go and the authtest package, neither of which any
// non-test package may import.
func TestProductionBinariesLinkNoAuthTestDoubles(t *testing.T) {
	// Go's test cache does not track files a child process reads. Reading the
	// cmd/ sources here makes an edit to them invalidate a cached pass.
	require.NoError(t, filepath.WalkDir("../../cmd", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		_, err = os.ReadFile(path)
		return err
	}))

	// pgx5 is the tag the Dockerfiles build with.
	cmd := exec.Command("go", "list", "-tags=pgx5", "-deps", "-f", "{{.ImportPath}}", "../../cmd/...")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	for _, pkg := range strings.Fields(string(out)) {
		require.NotContains(t, pkg, "github.com/stretchr/testify", "production binary links a test-only dependency")
		require.NotContains(t, pkg, "internal/auth/authtest", "production binary links the auth test doubles")
	}
}
