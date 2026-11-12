// Command fieldkeycheck lists composite literals of the given struct types
// that are not fully keyed, so a struct can be reordered for fieldalignment
// without silently changing positional literals.
//
// Input lines look like `path:line:col: struct ...` (analyzer output), read
// from the file argument or stdin. Exit 1 when unkeyed literals are found.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"golang.org/x/tools/go/packages"
)

var posRe = regexp.MustCompile(`^(.+?):(\d+):\d+`)

type lineKey struct {
	file string
	line int
}

func main() {
	os.Exit(run())
}

func run() int {
	dir := flag.String("dir", ".", "module root to load ./... from")
	tags := flag.String("tags", "integration", "build tags passed to the loader")
	flag.Parse()

	var in io.Reader = os.Stdin
	if flag.NArg() > 0 {
		f, err := os.Open(flag.Arg(0))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		defer f.Close()
		in = f
	}
	positions, err := readPositions(in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	hits, err := check(*dir, *tags, positions)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	for _, h := range hits {
		fmt.Println(h)
	}
	if len(hits) > 0 {
		return 1
	}
	return 0
}

func readPositions(r io.Reader) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if posRe.MatchString(sc.Text()) {
			out = append(out, sc.Text())
		}
	}
	return out, sc.Err()
}

// canon resolves symlinks so loader paths and input paths compare equal.
func canon(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func parseTargets(dir string, positions []string) (map[lineKey]bool, error) {
	targets := map[lineKey]bool{}
	for _, p := range positions {
		m := posRe.FindStringSubmatch(p)
		if m == nil {
			return nil, fmt.Errorf("unparseable position %q", p)
		}
		line, err := strconv.Atoi(m[2])
		if err != nil {
			return nil, err
		}
		file := m[1]
		if !filepath.IsAbs(file) {
			file = filepath.Join(dir, file)
		}
		abs, err := filepath.Abs(file)
		if err != nil {
			return nil, err
		}
		targets[lineKey{canon(abs), line}] = true
	}
	return targets, nil
}

// typeKey names a type independently of the loader's per-variant type
// universes (Tests: true type-checks a package more than once).
func typeKey(t types.Type) string {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	return types.TypeString(t, nil)
}

// check returns the sorted, de-duplicated positions of unkeyed literals of
// the structs declared at the given positions.
func check(dir, tags string, positions []string) ([]string, error) {
	if len(positions) == 0 {
		return nil, nil
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	targets, err := parseTargets(absDir, positions)
	if err != nil {
		return nil, err
	}
	pkgs, err := load(absDir, tags)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, p := range pkgs {
		collectTargets(p, targets, wanted)
	}
	if len(wanted) == 0 {
		return nil, errors.New("no struct declaration found at any input position")
	}
	return findUnkeyed(pkgs, wanted), nil
}

func findUnkeyed(pkgs []*packages.Package, wanted map[string]bool) []string {
	seen := map[string]bool{}
	for _, p := range pkgs {
		for _, f := range p.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				if lit, ok := n.(*ast.CompositeLit); ok && hasUnkeyed(lit) {
					tv, ok := p.TypesInfo.Types[lit]
					if ok && tv.Type != nil && wanted[typeKey(tv.Type)] {
						seen[canonPos(p.Fset, lit.Pos())] = true
					}
				}
				return true
			})
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func load(dir, tags string) ([]*packages.Package, error) {
	cfg := &packages.Config{
		Mode:       packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:        dir,
		Tests:      true,
		BuildFlags: []string{"-tags=" + tags},
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, err
	}
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			return nil, fmt.Errorf("load %s: %w", p.PkgPath, p.Errors[0])
		}
	}
	return pkgs, nil
}

func canonPos(fset *token.FileSet, pos token.Pos) string {
	return fset.Position(pos).String()
}

func collectTargets(p *packages.Package, targets map[lineKey]bool, wanted map[string]bool) {
	at := func(pos token.Pos) bool {
		ps := p.Fset.Position(pos)
		return targets[lineKey{canon(ps.Filename), ps.Line}]
	}
	for _, f := range p.Syntax {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.TypeSpec:
				if st, ok := n.Type.(*ast.StructType); ok && (at(st.Struct) || at(n.Pos())) {
					if obj := p.TypesInfo.Defs[n.Name]; obj != nil {
						wanted[typeKey(obj.Type())] = true
					}
				}
			case *ast.StructType:
				if at(n.Struct) {
					if tv, ok := p.TypesInfo.Types[n]; ok {
						wanted[typeKey(tv.Type)] = true
					}
				}
			}
			return true
		})
	}
}

func hasUnkeyed(lit *ast.CompositeLit) bool {
	for _, e := range lit.Elts {
		if _, ok := e.(*ast.KeyValueExpr); !ok {
			return true
		}
	}
	return false
}
