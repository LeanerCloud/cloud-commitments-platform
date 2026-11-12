package database

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

type diagnosticSingle struct{ next error }

func (*diagnosticSingle) Error() string   { return "chain" }
func (e *diagnosticSingle) Unwrap() error { return e.next }

type diagnosticNilSlice []error

func (diagnosticNilSlice) Error() string   { panic("nil slice Error called") }
func (diagnosticNilSlice) Unwrap() []error { panic("nil slice Unwrap called") }

type diagnosticNilMap map[string]error

func (diagnosticNilMap) Error() string { panic("nil map Error called") }
func (diagnosticNilMap) Is(error) bool { panic("nil map Is called") }

type diagnosticNilFunc func()

func (diagnosticNilFunc) Error() string { panic("nil func Error called") }
func (diagnosticNilFunc) As(any) bool   { panic("nil func As called") }

type diagnosticNilChan chan error

func (diagnosticNilChan) Error() string { panic("nil chan Error called") }
func (diagnosticNilChan) Unwrap() error { panic("nil chan Unwrap called") }

type diagnosticCustom struct {
	is         error
	pg         *pgconn.PgError
	connection *pgconn.ConnectError
	matchNil   bool
	children   []error
}

func (*diagnosticCustom) Error() string          { return "custom" }
func (e *diagnosticCustom) Is(target error) bool { return e.is == target }
func (e *diagnosticCustom) Unwrap() []error      { return e.children }
func (e *diagnosticCustom) As(target any) bool {
	switch target := target.(type) {
	case **pgconn.PgError:
		*target = e.pg
		return e.pg != nil || e.matchNil
	case **pgconn.ConnectError:
		*target = e.connection
		return e.connection != nil || e.matchNil
	default:
		return false
	}
}

func TestAPIDatabaseDiagnosticsUsageNilChains(t *testing.T) {
	var nilPG *pgconn.PgError
	var nilConnection *pgconn.ConnectError
	nils := []error{nilPG, nilConnection, diagnosticNilSlice(nil), diagnosticNilMap(nil), diagnosticNilFunc(nil), diagnosticNilChan(nil)}
	for _, nilError := range nils {
		for _, tree := range []error{nilError, &diagnosticSingle{next: nilError}, errors.Join(nilError), errors.Join(&diagnosticSingle{next: errors.Join(nilError)})} {
			require.NotPanics(t, func() {
				d := projectDiagnostic("Query", map[string]any{"err": tree})
				require.Equal(t, "other", d.class)
				require.Empty(t, d.sqlState)
			})
		}
	}
	first := &pgconn.PgError{Code: "P0001", Message: "unchanged-first", Detail: "unchanged-detail"}
	second := &pgconn.PgError{Code: "P0002"}
	for _, match := range []struct {
		err         error
		class, code string
	}{
		{context.DeadlineExceeded, "deadline", ""},
		{context.Canceled, "canceled", ""},
		{first, "database", "P0001"},
	} {
		for _, tree := range []error{
			errors.Join(nilConnection, match.err), errors.Join(match.err, nilConnection),
			errors.Join(nilPG, &diagnosticSingle{next: errors.Join(nilConnection, match.err)}, nilPG),
		} {
			d := projectDiagnostic("Query", map[string]any{"err": tree})
			require.Equal(t, match.class, d.class)
			require.Equal(t, match.code, d.sqlState)
		}
	}
	for _, tree := range []error{errors.Join(first, second), errors.Join(nilConnection, first, nilPG, second), &diagnosticCustom{matchNil: true, children: []error{nilPG, first, second}}} {
		facts := collectDiagnosticFacts(tree)
		require.Same(t, first, facts.pg)
		require.Equal(t, "P0001", projectDiagnostic("Query", map[string]any{"err": tree}).sqlState)
	}
	invalidFirst := &pgconn.PgError{Code: "invalid"}
	facts := collectDiagnosticFacts(errors.Join(invalidFirst, first))
	require.Same(t, invalidFirst, facts.pg)
	require.Empty(t, projectDiagnostic("Query", map[string]any{"err": errors.Join(invalidFirst, first)}).sqlState)
	require.Equal(t, "unchanged-first", first.Message)
	require.Equal(t, "unchanged-detail", first.Detail)
}

func TestAPIDatabaseDiagnosticsUsageMatchingPriority(t *testing.T) {
	pg := &pgconn.PgError{Code: "P0001"}
	connection := &pgconn.ConnectError{}
	for _, tree := range []error{
		&diagnosticSingle{next: context.Canceled},
		errors.Join(pg, context.Canceled), errors.Join(context.Canceled, pg, context.DeadlineExceeded),
		&diagnosticCustom{is: context.DeadlineExceeded},
		&diagnosticCustom{pg: pg},
		&diagnosticCustom{connection: connection},
		&diagnosticCustom{connection: connection, children: []error{pg}},
		errors.Join(pg, &pgconn.PgError{Code: "P0002"}),
	} {
		wantClass, wantCode := "other", ""
		var standardPG *pgconn.PgError
		var standardConnection *pgconn.ConnectError
		switch {
		case errors.Is(tree, context.DeadlineExceeded):
			wantClass = "deadline"
		case errors.Is(tree, context.Canceled):
			wantClass = "canceled"
		case errors.As(tree, &standardPG):
			wantClass, wantCode = "database", standardPG.Code
		case errors.As(tree, &standardConnection):
			wantClass = "connection"
		}
		d := projectDiagnostic("Query", map[string]any{"err": tree})
		require.Equal(t, wantClass, d.class)
		require.Equal(t, wantCode, d.sqlState)
		facts := collectDiagnosticFacts(tree)
		if standardPG != nil {
			require.Same(t, standardPG, facts.pg)
		}
		if standardConnection != nil {
			require.Same(t, standardConnection, facts.connection)
		}
	}
}

func TestAPIDatabaseDiagnosticsUsageVisitBudget(t *testing.T) {
	cycle := &diagnosticSingle{}
	cycle.next = cycle
	pg := &pgconn.PgError{Code: "P0001"}
	wide := make([]error, diagnosticVisitBudget+10)
	wide[len(wide)-1] = context.DeadlineExceeded
	deep := error(context.DeadlineExceeded)
	for range diagnosticVisitBudget + 1 {
		deep = &diagnosticSingle{next: deep}
	}
	for _, tree := range []error{cycle, &diagnosticCustom{children: wide}, deep} {
		d := projectDiagnostic("Query", map[string]any{"err": tree})
		require.Equal(t, "other", d.class, "unvisited tail cannot be claimed discovered")
		require.Empty(t, d.sqlState)
	}
	for _, tc := range []struct {
		tree        error
		class, code string
	}{
		{errors.Join(pg, cycle), "database", "P0001"},
		{errors.Join(context.DeadlineExceeded, cycle), "deadline", ""},
		{errors.Join(pg, context.Canceled, cycle), "canceled", ""},
	} {
		d := projectDiagnostic("Query", map[string]any{"err": tc.tree})
		require.Equal(t, tc.class, d.class)
		require.Equal(t, tc.code, d.sqlState)
	}
}
