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
	"internal/purchase.newAccountExecution",
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
	updatePurchaseExecutions = regexp.MustCompile(`(?is)\bUPDATE\s+purchase_executions\b.*?\bSET\b`)
	upsertSet                = regexp.MustCompile(`(?is)\bDO\s+UPDATE\s+SET\b`)
	setTerminator            = regexp.MustCompile(`(?i)^(WHERE|RETURNING)\b`)
	idempotencyKeyColumn     = regexp.MustCompile(`(?i)\bidempotency_key\b`)
	keyAssignmentTarget      = regexp.MustCompile(`(?i)^"?idempotency_key"?\s*=`)
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

// setText returns the SET clause text in rest (already masked by maskSQL), cut at the first WHERE, RETURNING or ";"
// outside parentheses, so a subselect's WHERE does not hide a later assignment.
func setText(rest string) string {
	depth, inQuote := 0, false
	for i, r := range rest {
		switch {
		case r == '\'':
			inQuote = !inQuote
		case inQuote:
		case r == '(':
			depth++
		case r == ')':
			depth--
		case depth == 0 && r == ';':
			return rest[:i]
		case depth == 0 && (i == 0 || !isWordByte(rest[i-1])) && setTerminator.MatchString(rest[i:]):
			return rest[:i]
		}
	}
	return rest
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// setItems splits a SET clause at commas outside parentheses and '...' literals.
func setItems(set string) []string {
	var items []string
	depth, start, inQuote := 0, 0, false
	for i, r := range set {
		switch {
		case r == '\'':
			inQuote = !inQuote
		case inQuote:
		case r == '(':
			depth++
		case r == ')':
			depth--
		case r == ',' && depth == 0:
			items = append(items, set[start:i])
			start = i + 1
		}
	}
	return append(items, set[start:])
}

// setsIdempotencyKey reports whether a SET clause assigns the column: as a plain target, or
// inside the column list of a row assignment. A reference in a value or subquery does not count.
func setsIdempotencyKey(set string) bool {
	for _, item := range setItems(set) {
		item = strings.TrimSpace(item)
		if !strings.HasPrefix(item, "(") {
			if keyAssignmentTarget.MatchString(item) {
				return true
			}
			continue
		}
		cols, tail, ok := columnList(item)
		if ok && strings.HasPrefix(strings.TrimSpace(tail), "=") && idempotencyKeyColumn.MatchString(cols) {
			return true
		}
	}
	return false
}

var purchaseExecutionsMention = regexp.MustCompile(`(?i)purchase_executions`)

// canOpenQuote reports whether a quote or prefix may start at i: the previous byte is not part of a word.
func canOpenQuote(sql string, i int) bool {
	if i == 0 {
		return true
	}
	p := sql[i-1]
	return !isWordByte(p) && p != '$' && p < 0x80
}

// quotedEnd returns the index after the closing quote of the literal whose body starts at start;
// a doubled quote is an escape, and a backslash escapes the next byte only when backslash is set.
func quotedEnd(sql string, start int, quote byte, backslash bool) (int, bool) {
	for i := start; i < len(sql); i++ {
		switch {
		case backslash && sql[i] == '\\':
			i++
		case sql[i] != quote:
		case i+1 < len(sql) && sql[i+1] == quote:
			i++
		default:
			return i + 1, true
		}
	}
	return 0, false
}

// dollarTag returns the "$tag$" opener at the start of s, or "" when s does not start one.
func dollarTag(s string) string {
	j := 1
	if j < len(s) && (s[j] == '_' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= 0x80) {
		for j < len(s) && (isWordByte(s[j]) || s[j] >= 0x80) {
			j++
		}
	}
	if j < len(s) && s[j] == '$' {
		return s[:j+1]
	}
	return ""
}

// blockCommentEnd returns the index after the nesting block comment that starts at i.
func blockCommentEnd(sql string, i int) (int, bool) {
	for depth := 0; i < len(sql); {
		switch {
		case strings.HasPrefix(sql[i:], "/*"):
			depth++
			i += 2
		case strings.HasPrefix(sql[i:], "*/"):
			depth--
			i += 2
			if depth == 0 {
				return i, true
			}
		default:
			i++
		}
	}
	return 0, false
}

// maskSQL replaces comments with a space and string literal bodies ('...', E'...', $$...$$, $tag$...$tag$)
// with an empty literal, so the scan never reads text that is not code. A "..." identifier is kept only
// for idempotency_key and is otherwise emitted as "x". ok is false when a literal, comment or identifier
// is unterminated, or a literal body mentions purchase_executions (SQL the scan cannot see).
// Plain '...' literals take a backslash literally (standard_conforming_strings on); U&'...' is not lexed.
func maskSQL(sql string) (masked string, ok bool) {
	var b strings.Builder
	for i := 0; i < len(sql); {
		var end, bodyStart, bodyEnd int
		var found bool
		c := sql[i]
		switch {
		case strings.HasPrefix(sql[i:], "--"):
			end = strings.IndexByte(sql[i:], '\n')
			if end < 0 {
				end = len(sql) - i
			}
			b.WriteByte(' ')
			i += end
			continue
		case strings.HasPrefix(sql[i:], "/*"):
			if end, found = blockCommentEnd(sql, i); !found {
				return "", false
			}
			b.WriteByte(' ')
			i = end
			continue
		case c == '"':
			if end, found = quotedEnd(sql, i+1, '"', false); !found {
				return "", false
			}
			if strings.EqualFold(sql[i+1:end-1], "idempotency_key") {
				b.WriteString(sql[i:end])
			} else {
				b.WriteString(`"x"`)
			}
			i = end
			continue
		case c == '\'':
			bodyStart = i + 1
			end, found = quotedEnd(sql, bodyStart, '\'', false)
			bodyEnd = end - 1
		case (c == 'E' || c == 'e') && canOpenQuote(sql, i) && i+1 < len(sql) && sql[i+1] == '\'':
			bodyStart = i + 2
			end, found = quotedEnd(sql, bodyStart, '\'', true)
			bodyEnd = end - 1
		case c == '$' && canOpenQuote(sql, i) && dollarTag(sql[i:]) != "":
			tag := dollarTag(sql[i:])
			bodyStart = i + len(tag)
			if k := strings.Index(sql[bodyStart:], tag); k >= 0 {
				bodyEnd, end, found = bodyStart+k, bodyStart+k+len(tag), true
			}
		default:
			b.WriteByte(c)
			i++
			continue
		}
		if !found || purchaseExecutionsMention.MatchString(sql[bodyStart:bodyEnd]) {
			return "", false
		}
		b.WriteString("''")
		i = end
	}
	return b.String(), true
}

// sqlIdempotencyKeyWriters counts purchase_executions INSERTs carrying idempotency_key
// (no column list counts) and UPDATEs or upserts that assign it in SET, after maskSQL removes comments and
// literal bodies. dynamic counts unverifiable INSERTs and statements maskSQL cannot lex.
func sqlIdempotencyKeyWriters(sql string) (inserts, updates, dynamic int) {
	masked, ok := maskSQL(sql)
	if !ok {
		if purchaseExecutionsMention.MatchString(sql) {
			dynamic++
		}
		return 0, 0, dynamic
	}
	sql = masked
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
		for _, loc := range upsertSet.FindAllStringIndex(tail, -1) {
			if setsIdempotencyKey(setText(tail[loc[1]:])) {
				updates++
			}
		}
	}
	for _, loc := range updatePurchaseExecutions.FindAllStringIndex(sql, -1) {
		if setsIdempotencyKey(setText(sql[loc[1]:])) {
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
		{"update key set after a WHERE inside parentheses", `UPDATE purchase_executions SET x = (SELECT a FROM t WHERE b = 1), idempotency_key = $2 WHERE id = $1`, 0, 1, 0},
		{"upsert key set after a WHERE inside parentheses", `INSERT INTO purchase_executions (execution_id) VALUES ($1) ON CONFLICT (execution_id) DO UPDATE SET x = (SELECT a FROM t WHERE b = 1), idempotency_key = $2`, 0, 1, 0},
		{"update filters on the key inside a SET subquery", `UPDATE purchase_executions SET a = (SELECT x FROM t WHERE idempotency_key = $1) WHERE execution_id = $2`, 0, 0, 0},
		{"update sets the key after a subquery that filters on it", `UPDATE purchase_executions SET a = (SELECT x FROM t WHERE idempotency_key = $1), idempotency_key = $2 WHERE execution_id = $3`, 0, 1, 0},
		{"update row assignment names the key", `UPDATE purchase_executions SET (status, idempotency_key) = ($1, $2) WHERE execution_id = $3`, 0, 1, 0},
		{"update row assignment without the key", `UPDATE purchase_executions SET (status, error) = (SELECT s, idempotency_key FROM t) WHERE execution_id = $1`, 0, 0, 0},
		{"update quoted identifier target", `UPDATE purchase_executions SET "idempotency_key" = $1 WHERE execution_id = $2`, 0, 1, 0},
		{"update target without space before equals", `UPDATE purchase_executions SET IDEMPOTENCY_KEY=$1 WHERE execution_id = $2`, 0, 1, 0},
		{"update literal with a paren and comma before the key target", `UPDATE purchase_executions SET error = 'x (', idempotency_key = $2 WHERE execution_id = $1`, 0, 1, 0},
		{"update literal that mentions WHERE before the key target", `UPDATE purchase_executions SET error = 'see WHERE', idempotency_key = $1 WHERE execution_id = $2`, 0, 1, 0},
		{"update literal that mentions the key", `UPDATE purchase_executions SET error = 'idempotency_key = 1, y' WHERE execution_id = $1`, 0, 0, 0},
		{"update with the key set twice counts once", `UPDATE purchase_executions SET idempotency_key = $1, idempotency_key = $2`, 0, 1, 0},
		{"upsert filters on the key inside a SET subquery", `INSERT INTO purchase_executions (execution_id) VALUES ($1) ON CONFLICT (execution_id) DO UPDATE SET a = (SELECT x FROM t WHERE idempotency_key = $2)`, 0, 0, 0},
		{"insert column list built with Sprintf", `INSERT INTO purchase_executions (%s) VALUES (%s)`, 0, 0, 1},
		{"insert column list unclosed by concatenation", `INSERT INTO purchase_executions (execution_id, `, 0, 0, 1},
		{"insert column list empty before concatenation", `INSERT INTO purchase_executions (`, 0, 0, 1},
		{"comment before the key target", `UPDATE purchase_executions SET /* k */ idempotency_key = $1`, 0, 1, 0},
		{"line comment before the key target", "UPDATE purchase_executions SET status = $3, -- note\n idempotency_key = $1 WHERE id = $2", 0, 1, 0},
		{"nested block comment before the key target", `UPDATE purchase_executions SET /* a /* b */ c */ idempotency_key = $1`, 0, 1, 0},
		{"commented-out assignment", "UPDATE purchase_executions SET status = $1 -- note, idempotency_key = $2\n WHERE x = $3", 0, 0, 0},
		{"comment marker inside a literal", `UPDATE purchase_executions SET error = 'a -- b', idempotency_key = $1`, 0, 1, 0},
		{"E string with an escaped quote", `UPDATE purchase_executions SET error = E'it\'s', idempotency_key = $1`, 0, 1, 0},
		{"dollar quoted body with a paren and WHERE", `UPDATE purchase_executions SET error = $$ WHERE ( $$, idempotency_key = $1`, 0, 1, 0},
		{"tagged dollar quote", `UPDATE purchase_executions SET error = $q$ x, WHERE ( $q$, idempotency_key = $1`, 0, 1, 0},
		{"E string that mentions the key", `UPDATE purchase_executions SET error = E'idempotency_key = 1'`, 0, 0, 0},
		{"unterminated literal on the table", `UPDATE purchase_executions SET error = 'abc`, 0, 0, 1},
		{"unterminated block comment on the table", `UPDATE purchase_executions SET x = 1 /* oops`, 0, 0, 1},
		{"unterminated literal off the table", `SELECT 'abc`, 0, 0, 0},
		{"trailing line comment at EOF", `UPDATE purchase_executions SET idempotency_key = $1 -- note`, 0, 1, 0},
		{"upsert comment before the key target", `INSERT INTO purchase_executions (execution_id) VALUES ($1) ON CONFLICT (execution_id) DO UPDATE SET /* k */ idempotency_key = $2`, 0, 1, 0},
		{"parameter is not a dollar quote", `UPDATE purchase_executions SET a = $1, idempotency_key = $2`, 0, 1, 0},
		{"adjacent parameters are not a dollar quote", `UPDATE purchase_executions SET a = $1$2, idempotency_key = $3`, 0, 1, 0},
		{"dollars inside a word do not open a quote", `UPDATE purchase_executions SET x$$y = $1, idempotency_key = $2`, 0, 1, 0},
		{"quoted identifier with comment marker", `UPDATE purchase_executions SET "a--b" = $1, idempotency_key = $2`, 0, 1, 0},
		{"quoted identifier with a quote", `UPDATE purchase_executions SET "o'k" = $1, idempotency_key = $2`, 0, 1, 0},
		{"quoted identifier with a paren", `UPDATE purchase_executions SET "a(" = $1, idempotency_key = $2`, 0, 1, 0},
		{"quoted key as the only target", `UPDATE purchase_executions SET "idempotency_key" = $1`, 0, 1, 0},
		{"SQL inside a dollar quoted body is unverifiable", `DO $$ UPDATE purchase_executions SET idempotency_key = $1 $$`, 0, 0, 1},
		{"insert with a comment between keywords", `INSERT /* c */ INTO purchase_executions (idempotency_key) VALUES ($1)`, 1, 0, 0},
		{"backslash is literal in a plain literal", `UPDATE purchase_executions SET error = 'a\', idempotency_key = $1`, 0, 1, 0},
		{"e quote inside a word is not an E string", `UPDATE purchase_executions SET error = type'a\', idempotency_key = $1`, 0, 1, 0},
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
