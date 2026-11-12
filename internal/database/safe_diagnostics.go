package database

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type safeDiagnosticsKey struct{}

// WithSafeDiagnostics selects coded database diagnostics without changing lifecycle.
func WithSafeDiagnostics(ctx context.Context) context.Context {
	if ctx.Value(safeDiagnosticsKey{}) == true {
		return ctx
	}
	return context.WithValue(ctx, safeDiagnosticsKey{}, true)
}

type databaseDiagnostic struct {
	event, class, sqlState string
	elapsed                time.Duration
	hasElapsed             bool
}

func projectDiagnostic(msg string, data map[string]any) databaseDiagnostic {
	d := databaseDiagnostic{event: diagnosticEvent(msg), class: "ok"}
	if raw := data["err"]; raw != nil {
		err, _ := raw.(error)
		d.class, d.sqlState = collectDiagnosticFacts(err).classification()
	}
	d.elapsed, d.hasElapsed = diagnosticElapsed(data["time"])
	return d
}

func diagnosticEvent(msg string) string {
	switch msg {
	case "Query", "BatchQuery", "BatchClose", "CopyFrom", "Connect", "Prepare", "Acquire", "Release":
		return msg
	default:
		return "Database"
	}
}

func diagnosticElapsed(value any) (time.Duration, bool) {
	elapsed, ok := value.(time.Duration)
	return elapsed, ok && elapsed >= 0
}

type diagnosticFacts struct {
	deadline, canceled bool
	pg                 *pgconn.PgError
	connection         *pgconn.ConnectError
}

const diagnosticVisitBudget = 64

type diagnosticChildren struct {
	values []error
	next   int
}

// The finite walk retains observed facts; an unvisited tail can contain higher priority matches.
func collectDiagnosticFacts(root error) diagnosticFacts {
	var facts diagnosticFacts
	stack := []diagnosticChildren{{values: []error{root}}}
	for remaining := diagnosticVisitBudget; remaining > 0 && len(stack) > 0; {
		top := &stack[len(stack)-1]
		if top.next == len(top.values) {
			stack = stack[:len(stack)-1]
			continue
		}
		node := top.values[top.next]
		top.next++
		remaining--
		if diagnosticNil(node) {
			continue
		}
		facts.match(node)
		stack = append(stack, diagnosticChildren{values: diagnosticUnwrap(node)})
	}
	return facts
}

func diagnosticNil(err error) bool {
	if err == nil {
		return true
	}
	v := reflect.ValueOf(err)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return v.IsNil()
	default:
		return false
	}
}

func diagnosticUnwrap(err error) []error {
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		return wrapped.Unwrap()
	case interface{ Unwrap() error }:
		return []error{wrapped.Unwrap()}
	default:
		return nil
	}
}

func diagnosticMatches(err, target error) bool {
	if reflect.TypeOf(err).Comparable() && err == target {
		return true
	}
	matcher, ok := err.(interface{ Is(error) bool })
	return ok && matcher.Is(target)
}

func (f *diagnosticFacts) match(err error) {
	f.deadline = f.deadline || diagnosticMatches(err, context.DeadlineExceeded)
	f.canceled = f.canceled || diagnosticMatches(err, context.Canceled)
	f.matchDatabase(err)
}

func (f *diagnosticFacts) matchDatabase(err error) {
	if f.pg == nil {
		f.pg = diagnosticPG(err)
	}
	if f.connection == nil {
		f.connection = diagnosticConnection(err)
	}
}

func diagnosticPG(err error) *pgconn.PgError {
	if direct, ok := err.(*pgconn.PgError); ok {
		return direct
	}
	var target *pgconn.PgError
	if matcher, ok := err.(interface{ As(any) bool }); ok && matcher.As(&target) {
		return target
	}
	return nil
}

func diagnosticConnection(err error) *pgconn.ConnectError {
	if direct, ok := err.(*pgconn.ConnectError); ok {
		return direct
	}
	var target *pgconn.ConnectError
	if matcher, ok := err.(interface{ As(any) bool }); ok && matcher.As(&target) {
		return target
	}
	return nil
}

func (f diagnosticFacts) classification() (string, string) {
	switch {
	case f.deadline:
		return "deadline", ""
	case f.canceled:
		return "canceled", ""
	case f.pg != nil:
		return "database", diagnosticSQLState(f.pg.Code)
	case f.connection != nil:
		return "connection", ""
	default:
		return "other", ""
	}
}

func diagnosticSQLState(code string) string {
	if len(code) != 5 {
		return ""
	}
	for _, c := range code {
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return ""
		}
	}
	return code
}

func (d databaseDiagnostic) text() string {
	text := "Database event=" + d.event + " class=" + d.class
	if d.sqlState != "" {
		text += " sqlstate=" + d.sqlState
	}
	if d.hasElapsed {
		text += fmt.Sprintf(" duration=%s", d.elapsed)
	}
	return text
}
