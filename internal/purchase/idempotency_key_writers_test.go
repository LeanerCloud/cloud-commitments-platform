package purchase

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const idempotencyKeySettleContract = "A purchase lineage's IdempotencyKey is minted once and copied verbatim; the provider token and the go#107 " +
	"attempt journal's settle contract key on it. If this writer is intended, review it against that contract and then update the pinned set."

// pinnedIdempotencyKeyWriters is every function that writes PurchaseExecution.IdempotencyKey.
var pinnedIdempotencyKeyWriters = []string{
	"internal/api.Handler.persistRetryExecution",
	"internal/config.PostgresStore.SavePurchaseExecutionTx",
	"internal/config.scanExecutionRows",
	"internal/purchase.Manager.executeForAccount",
	"internal/purchase.Manager.writeAttached",
}

func isIdempotencyKeySelector(e ast.Expr) bool {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			break
		}
		e = p.X
	}
	sel, ok := e.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "IdempotencyKey"
}

func receiverTypeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.IndexExpr:
		return receiverTypeName(t.X)
	case *ast.IndexListExpr:
		return receiverTypeName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return "?"
}

func declLabel(dir string, d ast.Decl) string {
	fn, ok := d.(*ast.FuncDecl)
	if !ok {
		return dir + ".<package-level>"
	}
	if fn.Recv != nil && len(fn.Recv.List) == 1 {
		return dir + "." + receiverTypeName(fn.Recv.List[0].Type) + "." + fn.Name.Name
	}
	return dir + "." + fn.Name.Name
}

// idempotencyKeyWriters lists the declarations that assign, take the address of, or
// key a literal with IdempotencyKey, by identifier. Blind: whole-struct copies, SQL from non-literal pieces.
func idempotencyKeyWriters(f *ast.File, dir string) []string {
	var out []string
	for _, d := range f.Decls {
		label := declLabel(dir, d)
		ast.Inspect(d, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.AssignStmt:
				for _, lhs := range x.Lhs {
					if isIdempotencyKeySelector(lhs) {
						out = append(out, label)
					}
				}
			case *ast.UnaryExpr:
				if x.Op == token.AND && isIdempotencyKeySelector(x.X) {
					out = append(out, label)
				}
			case *ast.KeyValueExpr:
				if id, ok := x.Key.(*ast.Ident); ok && id.Name == "IdempotencyKey" {
					out = append(out, label)
				}
			}
			return true
		})
	}
	return out
}

var (
	insertPurchaseExecutions = regexp.MustCompile(`(?is)\bINSERT\s+INTO\s+purchase_executions\b`)
	updatePurchaseExecutions = regexp.MustCompile(`(?is)\bUPDATE\s+purchase_executions\b.*?\bSET\b(.*?)(?:\bWHERE\b|\bRETURNING\b|;|$)`)
	upsertSet                = regexp.MustCompile(`(?is)\bDO\s+UPDATE\s+SET\b(.*?)(?:\bWHERE\b|\bRETURNING\b|;|$)`)
	idempotencyKeyColumn     = regexp.MustCompile(`(?i)\bidempotency_key\b`)
)

// columnList returns the balanced "(...)" after an INSERT target and the text
// following it; ok is false when the list is missing its close.
func columnList(rest string) (cols, tail string, ok bool) {
	depth := 0
	for i, r := range rest {
		switch r {
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return rest[:i+1], rest[i+1:], true
			}
		}
	}
	return "", "", false
}

// sqlIdempotencyKeyWriters counts purchase_executions INSERTs carrying idempotency_key
// (no column list counts) and UPDATEs or upserts that SET it; dynamic counts unverifiable INSERTs.
func sqlIdempotencyKeyWriters(sql string) (inserts, updates, dynamic int) {
	for _, loc := range insertPurchaseExecutions.FindAllStringIndex(sql, -1) {
		rest := strings.TrimSpace(sql[loc[1]:])
		if !strings.HasPrefix(rest, "(") {
			inserts++
			continue
		}
		cols, tail, ok := columnList(rest)
		if !ok || strings.Contains(cols, "%") || strings.TrimSpace(cols[1:len(cols)-1]) == "" {
			dynamic++
			continue
		}
		if idempotencyKeyColumn.MatchString(cols) {
			inserts++
		}
		if end := strings.Index(tail, ";"); end >= 0 {
			tail = tail[:end]
		}
		for _, m := range upsertSet.FindAllStringSubmatch(tail, -1) {
			if idempotencyKeyColumn.MatchString(m[1]) {
				updates++
			}
		}
	}
	for _, m := range updatePurchaseExecutions.FindAllStringSubmatch(sql, -1) {
		if idempotencyKeyColumn.MatchString(m[1]) {
			updates++
		}
	}
	return inserts, updates, dynamic
}

func sqlLiteralWriters(f *ast.File) (inserts, updates, dynamic int) {
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if s, err := strconv.Unquote(lit.Value); err == nil {
			i, u, d := sqlIdempotencyKeyWriters(s)
			inserts += i
			updates += u
			dynamic += d
		}
		return true
	})
	return inserts, updates, dynamic
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "go.mod not found above the test directory")
		dir = parent
	}
}

func TestIdempotencyKeyCopyHasSingleWriter(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	writers := map[string]bool{}
	var inserts, updates, dynamic, files int

	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "testdata") {
				return filepath.SkipDir
			}
			if _, statErr := os.Stat(filepath.Join(path, "go.mod")); path != root && statErr == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		files++
		rel, relErr := filepath.Rel(root, filepath.Dir(path))
		if relErr != nil {
			return relErr
		}
		for _, w := range idempotencyKeyWriters(f, filepath.ToSlash(rel)) {
			writers[w] = true
		}
		i, u, dyn := sqlLiteralWriters(f)
		inserts += i
		updates += u
		dynamic += dyn
		return nil
	}))

	require.Greater(t, files, 100, "the scan must cover the module, not a subtree")
	got := make([]string, 0, len(writers))
	for w := range writers {
		got = append(got, w)
	}
	sort.Strings(got)
	want := append([]string(nil), pinnedIdempotencyKeyWriters...)
	sort.Strings(want)
	assert.Equal(t, want, got, idempotencyKeySettleContract)
	assert.Zero(t, dynamic, "cannot verify idempotency_key writers in a dynamically built purchase_executions INSERT. "+idempotencyKeySettleContract)
	assert.Equal(t, 1, inserts, "exactly one INSERT INTO purchase_executions may carry idempotency_key. "+idempotencyKeySettleContract)
	assert.Equal(t, 0, updates, "no UPDATE purchase_executions may SET idempotency_key. "+idempotencyKeySettleContract)
}

func parseFixture(t *testing.T, src string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "fixture.go", src, parser.SkipObjectResolution)
	require.NoError(t, err)
	return f
}

func TestIdempotencyKeyWriterScan_Detects(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{"selector assignment", `package p
func f(e *X) { e.IdempotencyKey = "a" }`, []string{"d.f"}},
		{"composite literal key", `package p
func g() { _ = X{IdempotencyKey: "a"} }`, []string{"d.g"}},
		{"aliased import and method receiver", `package p
import cfg "example.com/config"
func (m *Manager) h(k string) { var e cfg.PurchaseExecution; e.IdempotencyKey = k; _ = &cfg.PurchaseExecution{IdempotencyKey: k} }`, []string{"d.Manager.h"}},
		{"address of field", `package p
func (s Store) scan(e *X) { _ = row.Scan(&e.IdempotencyKey) }`, []string{"d.Store.scan"}},
		{"parenthesised assignment", `package p
func p1(e *X) { (e.IdempotencyKey) = "a" }`, []string{"d.p1"}},
		{"package level literal", `package p
var def = X{IdempotencyKey: "a"}`, []string{"d.<package-level>"}},
		{"reads and declarations are not writers", `package p
type X struct{ IdempotencyKey string }
func r(e *X) string { k := e.IdempotencyKey; use(e.IdempotencyKey); return k }`, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.ElementsMatch(t, tc.want, dedupe(idempotencyKeyWriters(parseFixture(t, tc.src), "d")))
		})
	}
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func TestIdempotencyKeySQLScan_Detects(t *testing.T) {
	tests := []struct {
		name                      string
		sql                       string
		inserts, updates, dynamic int
	}{
		{"insert with column", `INSERT INTO purchase_executions (execution_id, idempotency_key) VALUES ($1, $2)`, 1, 0, 0},
		{"insert without the column", `INSERT INTO purchase_executions (execution_id, status) VALUES ($1, $2)`, 0, 0, 0},
		{"insert select copy", "insert into purchase_executions (idempotency_key, status)\nSELECT idempotency_key, 'x' FROM purchase_executions", 1, 0, 0},
		{"insert with no column list", `INSERT INTO purchase_executions SELECT * FROM purchase_executions`, 1, 0, 0},
		{"update sets it", `UPDATE purchase_executions SET status = $2, idempotency_key = $3 WHERE execution_id = $1`, 0, 1, 0},
		{"update only filters on it", `UPDATE purchase_executions SET status = $2 WHERE idempotency_key = $1`, 0, 0, 0},
		{"upsert update clause is not a separate update", `INSERT INTO purchase_executions (execution_id, idempotency_key) VALUES ($1, $2) ON CONFLICT (execution_id) DO UPDATE SET status = EXCLUDED.status`, 1, 0, 0},
		{"other table is out of scope", `INSERT INTO ri_exchange_idempotency (idempotency_key, claimed_at) VALUES ($1, now())`, 0, 0, 0},
		{"similarly named table is out of scope", `INSERT INTO purchase_execution_fan_out_shapes (idempotency_key, shape) VALUES ($1, $2)`, 0, 0, 0},
		{"upsert rewrites the key from EXCLUDED", `INSERT INTO purchase_executions (execution_id, status) VALUES ($1, $2) ON CONFLICT (execution_id) DO UPDATE SET idempotency_key = EXCLUDED.idempotency_key`, 0, 1, 0},
		{"upsert rewrites the key from a parameter", `INSERT INTO purchase_executions (execution_id) VALUES ($1) ON CONFLICT (execution_id) DO UPDATE SET status = $2, idempotency_key = $3 WHERE true`, 0, 1, 0},
		{"update row constructor", `UPDATE purchase_executions SET (idempotency_key, status) = ($2, $3) WHERE execution_id = $1`, 0, 1, 0},
		{"update key set after a FROM inside parentheses", `UPDATE purchase_executions SET status = (SELECT s FROM t), idempotency_key = $2 WHERE execution_id = $1`, 0, 1, 0},
		{"insert column list built with Sprintf", `INSERT INTO purchase_executions (%s) VALUES (%s)`, 0, 0, 1},
		{"insert column list unclosed by concatenation", `INSERT INTO purchase_executions (execution_id, `, 0, 0, 1},
		{"insert column list empty before concatenation", `INSERT INTO purchase_executions (`, 0, 0, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			i, u, d := sqlIdempotencyKeyWriters(tc.sql)
			assert.Equal(t, tc.dynamic, d, "dynamic")
			assert.Equal(t, tc.inserts, i, "inserts")
			assert.Equal(t, tc.updates, u, "updates")
		})
	}
}
