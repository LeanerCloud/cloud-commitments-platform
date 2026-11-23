// Package filesizeguard enforces the 500-line ceiling for non-test Go files
// under cmd/ and internal/ (CLAUDE.md) without a flag day: files already over
// the ceiling are listed in allowlist.txt with a cap, and the list may only
// shrink.
//
// To shrink it, split the file and delete its allowlist line in the same
// commit that brings it to 500 lines or fewer; a stale line fails the test.
// Raising a cap or adding a line is a reviewed edit that needs a reason.
package filesizeguard

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ceiling    = 500
	dateLayout = "2006-01-02"
	minScanned = 400 // sanity floor so a wrong root fails loud
)

var (
	scanRoots     = []string{"cmd", "internal"}
	skipDirs      = map[string]bool{"testdata": true, "node_modules": true, ".terraform": true}
	generatedLine = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)
)

type entry struct {
	path string
	cap  int
}

// parseAllowlist reads path<TAB>cap<TAB>date<TAB>reason lines; blank lines
// and # comments are ignored.
func parseAllowlist(r io.Reader) ([]entry, []error) {
	var entries []entry
	var errs []error
	prev := ""
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		e, err := parseEntry(line)
		if err != nil {
			errs = append(errs, fmt.Errorf("allowlist line %d: %w", n, err))
			continue
		}
		if e.path == prev {
			errs = append(errs, fmt.Errorf("allowlist line %d: duplicate path %s", n, e.path))
		} else if e.path < prev {
			errs = append(errs, fmt.Errorf("allowlist line %d: %s is not sorted after %s", n, e.path, prev))
		}
		prev = e.path
		entries = append(entries, e)
	}
	return entries, errs
}

func parseEntry(line string) (entry, error) {
	cols := strings.Split(line, "\t")
	if len(cols) != 4 {
		return entry{}, fmt.Errorf("want 4 tab-separated columns, got %d", len(cols))
	}
	limit, err := strconv.Atoi(cols[1])
	if err != nil || limit <= ceiling {
		return entry{}, fmt.Errorf("cap %q must be an integer above %d", cols[1], ceiling)
	}
	if _, err := time.Parse(dateLayout, cols[2]); err != nil {
		return entry{}, fmt.Errorf("date %q is not YYYY-MM-DD", cols[2])
	}
	if strings.TrimSpace(cols[3]) == "" {
		return entry{}, errors.New("reason is empty")
	}
	return entry{path: cols[0], cap: limit}, nil
}

// isGenerated reports a "Code generated ... DO NOT EDIT." header before the
// package clause.
func isGenerated(src string) bool {
	for _, line := range strings.SplitAfter(src, "\n") {
		line = strings.TrimRight(line, "\r\n")
		if generatedLine.MatchString(line) {
			return true
		}
		if strings.HasPrefix(line, "package ") {
			return false
		}
	}
	return false
}

// measure returns the newline count of every in-scope Go file.
func measure(fsys fs.FS) (map[string]int, error) {
	sizes := map[string]int{}
	for _, root := range scanRoots {
		err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
			if p == root && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll // absent root; TestGoFileSizeCeiling's floor catches a wrong repo root
			}
			if err != nil {
				return err
			}
			if d.IsDir() && skipDirs[d.Name()] {
				return fs.SkipDir
			}
			if d.IsDir() || path.Ext(p) != ".go" || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			if !isGenerated(string(b)) {
				sizes[p] = strings.Count(string(b), "\n")
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return sizes, nil
}

func checkListed(e entry, sizes map[string]int) error {
	size, ok := sizes[e.path]
	switch {
	case !ok:
		return fmt.Errorf("%s: listed but missing or out of scope; delete its allowlist line", e.path)
	case size <= ceiling:
		return fmt.Errorf("%s: now %d lines (<= %d); delete its allowlist line so the list only shrinks", e.path, size, ceiling)
	case size > e.cap:
		return fmt.Errorf("%s: grew to %d lines, past its cap %d; split it, or raise the cap with a new reason", e.path, size, e.cap)
	}
	return nil
}

// check applies every rule to fsys (rooted at the repository) and allowlist.
func check(fsys fs.FS, allowlist io.Reader) []error {
	entries, errs := parseAllowlist(allowlist)
	sizes, err := measure(fsys)
	if err != nil {
		return append(errs, err)
	}
	listed := map[string]bool{}
	for _, e := range entries {
		listed[e.path] = true
		if err := checkListed(e, sizes); err != nil {
			errs = append(errs, err)
		}
	}
	var paths []string
	for p := range sizes {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if sizes[p] > ceiling && !listed[p] {
			errs = append(errs, fmt.Errorf("%s: %d lines exceeds %d and is not allowlisted; split it, or add an allowlist line with a reason", p, sizes[p], ceiling))
		}
	}
	return errs
}

func mapFS(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for p, c := range files {
		m[p] = &fstest.MapFile{Data: []byte(c)}
	}
	return m
}

func lines(n int) string { return strings.Repeat("x\n", n) }

func row(p string, limit int) string {
	return fmt.Sprintf("%s\t%d\t2026-11-23\treason\n", p, limit)
}

func TestCheck(t *testing.T) {
	const big = "internal/a/big.go"
	tests := []struct {
		name  string
		files map[string]string
		allow string
		want  string // substring of the expected error, "" for pass
	}{
		{"exactly 500 passes", map[string]string{big: lines(500)}, "", ""},
		{"501 is a new offender", map[string]string{big: lines(501)}, "", "not allowlisted"},
		{"listed at cap passes", map[string]string{big: lines(600)}, row(big, 600), ""},
		{"growth past cap", map[string]string{big: lines(601)}, row(big, 600), "past its cap"},
		{"stale entry", map[string]string{big: lines(500)}, row(big, 600), "delete its allowlist line"},
		{"missing path", map[string]string{"internal/a/x.go": lines(1)}, row(big, 600), "missing"},
		{"generated file skipped", map[string]string{big: "// Code generated by x. DO NOT EDIT.\n\npackage a\n" + lines(900)}, "", ""},
		{"header after package does not skip", map[string]string{big: "package a\n// Code generated by x. DO NOT EDIT.\n" + lines(900)}, "", "not allowlisted"},
		{"test file skipped", map[string]string{"internal/a/big_test.go": lines(900)}, "", ""},
		{"outside roots skipped", map[string]string{"scripts/gen.go": lines(900)}, "", ""},
		{"testdata skipped", map[string]string{"internal/a/testdata/big.go": lines(900)}, "", ""},
		{"wrong column count", nil, "internal/a/big.go\t600\n", "4 tab-separated"},
		{"non-numeric cap", nil, "internal/a/big.go\tabc\t2026-11-23\treason\n", "cap"},
		{"cap at ceiling", nil, row(big, 500), "cap"},
		{"bad date", nil, "internal/a/big.go\t600\t23-11-2026\treason\n", "date"},
		{"empty reason", nil, "internal/a/big.go\t600\t2026-11-23\t \n", "reason"},
		{"duplicate", map[string]string{big: lines(600)}, row(big, 600) + row(big, 600), "duplicate"},
		{"unsorted", map[string]string{big: lines(600), "internal/b/big.go": lines(600)},
			row("internal/b/big.go", 600) + row(big, 600), "not sorted"},
		{"comments and blanks ignored", map[string]string{big: lines(600)}, "# c\n\n" + row(big, 600), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fsys := mapFS(tc.files)
			errs := check(fsys, strings.NewReader(tc.allow))
			if tc.want == "" {
				assert.Empty(t, errs)
				return
			}
			require.NotEmpty(t, errs)
			assert.ErrorContains(t, errors.Join(errs...), tc.want)
		})
	}
}

func TestGoFileSizeCeiling(t *testing.T) {
	root := os.DirFS("../..")
	sizes, err := measure(root)
	require.NoError(t, err)
	require.Greater(t, len(sizes), minScanned, "scanned too few files; wrong root?")

	f, err := os.Open("allowlist.txt")
	require.NoError(t, err)
	defer f.Close()
	for _, e := range check(root, f) {
		t.Error(e)
	}
}
