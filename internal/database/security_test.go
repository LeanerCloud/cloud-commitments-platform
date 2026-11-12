package database

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/tracelog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateRequiredFields_BothPasswordAndSecret_WarnOnStderr is a
// regression test for issue #441: when both DB_PASSWORD and DB_PASSWORD_SECRET
// are set, a warning must be emitted to stderr (not silently accepted, and not
// a hard error that breaks local-dev workflows).
func TestValidateRequiredFields_BothPasswordAndSecret_WarnOnStderr(t *testing.T) {
	// Capture stderr
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w

	cfg := &Config{
		Host:           "localhost",
		Database:       "testdb",
		User:           "testuser",
		Password:       "plaintext-pass",
		PasswordSecret: "arn:aws:secretsmanager:us-east-1:123:secret:mydb",
	}

	validateErr := cfg.validateRequiredFields()

	w.Close()
	os.Stderr = origStderr

	var stderrBuf bytes.Buffer
	_, _ = io.Copy(&stderrBuf, r)
	r.Close()

	// Must not return an error (should not break existing workflows).
	assert.NoError(t, validateErr,
		"both password sources set must not return an error (local-dev workflows must still work)")

	// Must emit a warning to stderr.
	stderrContent := stderrBuf.String()
	assert.Contains(t, stderrContent, "WARNING",
		"a warning must appear on stderr when both DB_PASSWORD and DB_PASSWORD_SECRET are set")
	assert.Contains(t, stderrContent, "DB_PASSWORD_SECRET",
		"warning must mention DB_PASSWORD_SECRET as the preferred path")
}

// TestValidateRequiredFields_OnlySecret_NoWarning verifies that using only
// DB_PASSWORD_SECRET (the preferred production path) produces no warning.
func TestValidateRequiredFields_OnlySecret_NoWarning(t *testing.T) {
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w

	cfg := &Config{
		Host:           "localhost",
		Database:       "testdb",
		User:           "testuser",
		Password:       "",
		PasswordSecret: "arn:aws:secretsmanager:us-east-1:123:secret:mydb",
	}

	validateErr := cfg.validateRequiredFields()

	w.Close()
	os.Stderr = origStderr

	var stderrBuf bytes.Buffer
	_, _ = io.Copy(&stderrBuf, r)
	r.Close()

	assert.NoError(t, validateErr)
	assert.NotContains(t, stderrBuf.String(), "WARNING",
		"no warning when only DB_PASSWORD_SECRET is set (preferred path)")
}

// TestBuildPoolConfig_ParseConfigUsesRedactedPassword is a regression test for
// issue #444: pgxpool.ParseConfig must never receive a DSN that contains the
// real password. If the parse fails, the error chain must not expose the
// plaintext credential.
//
// The fix parses a DSN containing the placeholder "REDACTED" and then sets
// ConnConfig.Password to the real credential. We verify that the real password
// does not appear in the poolConfig's DSN-derived fields by confirming that a
// deliberately bad host/DSN triggers a parse error whose text does not contain
// the real password.
func TestBuildPoolConfig_ParseConfigDoesNotExposePassword(t *testing.T) {
	const realPassword = "SUPER_SENSITIVE_DB_PASS_12345"

	// Valid config — parse must succeed.
	cfg := &Config{
		Host:           "localhost",
		Port:           5432,
		User:           "testuser",
		Password:       realPassword,
		Database:       "testdb",
		SSLMode:        "disable",
		MaxConnections: 10,
		MinConnections: 1,
		ConnectTimeout: 5 * time.Second,
		LogLevel:       "info",
	}

	poolConfig, err := buildPoolConfig(cfg, realPassword)
	require.NoError(t, err)
	require.NotNil(t, poolConfig)

	// The real password must be present in ConnConfig.Password (used at connect time).
	assert.Equal(t, realPassword, poolConfig.ConnConfig.Password,
		"ConnConfig.Password must hold the real password for actual connections")

	// ConnConfig.Host must be set correctly (proves DSN was parsed successfully).
	assert.Equal(t, "localhost", poolConfig.ConnConfig.Host)
}

// TestBuildPoolConfig_ParseError_NoPasswordLeak verifies that when the DSN
// contains a structurally invalid piece (beyond what pgx can parse) any error
// returned does not expose the real password. This tests the defense-in-depth
// goal of issue #444: by passing "REDACTED" to ParseConfig, even an error from
// pgx's URI parser only shows "REDACTED", not the real credential.
func TestBuildPoolConfig_ParseError_NoPasswordLeak(t *testing.T) {
	const realPassword = "SUPER_SENSITIVE_DB_PASS_12345"

	// Use an invalid SSL mode to provoke an error from pgxpool.ParseConfig.
	// (pgx validates the sslmode string during parsing.)
	cfg := &Config{
		Host:           "localhost",
		Port:           5432,
		User:           "testuser",
		Password:       realPassword,
		Database:       "testdb",
		SSLMode:        "invalid-ssl-mode",
		MaxConnections: 10,
		MinConnections: 1,
		ConnectTimeout: 5 * time.Second,
		LogLevel:       "info",
	}

	_, err := buildPoolConfig(cfg, realPassword)
	// pgx rejects an unknown sslmode during ParseConfig, so an error is
	// guaranteed here. The real password must never surface in the error text.
	require.Error(t, err,
		"pgxpool.ParseConfig must reject sslmode=invalid-ssl-mode")
	assert.NotContains(t, err.Error(), realPassword,
		"parse error must not expose the real DB password in the error chain")
	// The placeholder "REDACTED" may appear, which is acceptable.
}

func TestSanitizeLogData_BoundArgumentsRequireExplicitOptIn(t *testing.T) {
	input := map[string]any{
		"args": []any{"session-token", "bcrypt-hash"},
		"sql":  "SELECT $1", "err": "query failed",
		"password": "private-password", "secret": "private-secret", "token": "private-token",
	}
	for _, optIn := range []string{"", "false", "TRUE", "1", "true"} {
		t.Run("bind_parameters="+optIn, func(t *testing.T) {
			t.Setenv("DB_LOG_BIND_PARAMETERS", optIn)
			safe := sanitizeLogData(input)
			assert.Equal(t, input["sql"], safe["sql"])
			assert.Equal(t, input["err"], safe["err"])
			for _, key := range []string{"password", "secret", "token"} {
				assert.NotContains(t, safe, key)
				assert.Contains(t, input, key)
			}
			if optIn == "true" {
				assert.Equal(t, input["args"], safe["args"])
			} else {
				assert.NotContains(t, safe, "args")
			}
			assert.Equal(t, []any{"session-token", "bcrypt-hash"}, input["args"])
			safe["sql"] = "changed"
			assert.Equal(t, "SELECT $1", input["sql"])
		})
	}
}

// TestIsSensitiveKey verifies the sensitive-key predicate covers all expected names.
func TestIsSensitiveKey(t *testing.T) {
	assert.True(t, isSensitiveKey("password"))
	assert.True(t, isSensitiveKey("secret"))
	assert.True(t, isSensitiveKey("token"))
	assert.False(t, isSensitiveKey("sql"))
	assert.False(t, isSensitiveKey("args"))
	assert.False(t, isSensitiveKey("host"))
}

func TestStdLogger_RedactsBoundArguments(t *testing.T) {
	var output bytes.Buffer
	previousOutput := logging.SetOutput(&output)
	previousLevel := logging.GetLevel()
	logging.SetLevelValue(logging.LevelDebug)
	t.Cleanup(func() {
		logging.SetOutput(previousOutput)
		logging.SetLevelValue(previousLevel)
	})

	data := map[string]any{
		"args":     []any{"session-token", "hash-value"},
		"sql":      "INSERT INTO sessions VALUES ($1, $2)",
		"password": "private-password", "secret": "private-secret", "token": "private-token",
	}
	for _, optIn := range []string{"", "true"} {
		t.Run("bind_parameters="+optIn, func(t *testing.T) {
			t.Setenv("DB_LOG_BIND_PARAMETERS", optIn)
			for _, level := range []tracelog.LogLevel{tracelog.LogLevelDebug, tracelog.LogLevelInfo, tracelog.LogLevelWarn, tracelog.LogLevelError} {
				t.Run(level.String(), func(t *testing.T) {
					output.Reset()
					(&stdLogger{}).Log(context.Background(), level, "Query", data)
					logged := output.String()
					assert.Contains(t, logged, "Query")
					for _, value := range []string{"private-password", "private-secret", "private-token"} {
						assert.NotContains(t, logged, value)
					}
					if optIn == "true" && level != tracelog.LogLevelInfo {
						assert.Contains(t, logged, "args:")
						assert.Contains(t, logged, "session-token")
						assert.Contains(t, logged, "hash-value")
					} else {
						assert.NotContains(t, logged, "args:")
						assert.NotContains(t, logged, "session-token")
						assert.NotContains(t, logged, "hash-value")
					}
					if level == tracelog.LogLevelInfo {
						assert.NotContains(t, logged, "sql:")
					} else {
						assert.Contains(t, logged, data["sql"])
					}
				})
			}
		})
	}
}

func TestSanitizeLogData_SensitiveKeysWithoutArgs(t *testing.T) {
	sensitive := map[string]any{
		"password": "my-password",
		"secret":   "my-secret",
		"token":    "my-token",
		"safe":     "visible",
	}

	for _, optIn := range []string{"", "true"} {
		t.Run("bind_parameters="+optIn, func(t *testing.T) {
			t.Setenv("DB_LOG_BIND_PARAMETERS", optIn)
			safe := sanitizeLogData(sensitive)
			assert.NotContains(t, safe, "password")
			assert.NotContains(t, safe, "secret")
			assert.NotContains(t, safe, "token")
			assert.Contains(t, safe, "safe")
		})
	}
}

type diagnosticsHostile struct {
	stringCalls atomic.Int64
	errorCalls  atomic.Int64
	formatCalls atomic.Int64
}

func (h *diagnosticsHostile) String() string {
	h.stringCalls.Add(1)
	return "forbidden-String"
}

func (h *diagnosticsHostile) Error() string {
	h.errorCalls.Add(1)
	return "forbidden-Error"
}

func (h *diagnosticsHostile) Format(state fmt.State, _ rune) {
	h.formatCalls.Add(1)
	_, _ = io.WriteString(state, "forbidden-Format")
}

type diagnosticsStringer struct{ hostile *diagnosticsHostile }

func (s diagnosticsStringer) String() string { return s.hostile.String() }

type diagnosticsError struct{ hostile *diagnosticsHostile }

func (e diagnosticsError) Error() string { return e.hostile.Error() }

type diagnosticsContextKey struct{}

func requireDiagnosticsUnformatted(t *testing.T, hostile *diagnosticsHostile) {
	t.Helper()
	require.Zero(t, hostile.stringCalls.Load())
	require.Zero(t, hostile.errorCalls.Load())
	require.Zero(t, hostile.formatCalls.Load())
}

func TestAPIDatabaseDiagnosticsUsageMatrix(t *testing.T) {
	var output bytes.Buffer
	oldOutput, oldLevel := logging.SetOutput(&output), logging.GetLevel()
	logging.SetLevelValue(logging.LevelDebug)
	t.Cleanup(func() { logging.SetOutput(oldOutput); logging.SetLevelValue(oldLevel) })
	for _, safe := range []bool{false, true} {
		for _, bind := range []string{"", "true"} {
			t.Setenv("DB_LOG_BIND_PARAMETERS", bind)
			for _, level := range []tracelog.LogLevel{tracelog.LogLevelDebug, tracelog.LogLevelInfo, tracelog.LogLevelWarn, tracelog.LogLevelError, tracelog.LogLevelTrace, tracelog.LogLevelNone} {
				hostile := &diagnosticsHostile{}
				ctx := context.Background()
				data := map[string]any{"sql": "raw-sql", "args": []any{"raw-key", "raw-hash"}, "err": errors.New("raw-error"), "time": time.Millisecond}
				if safe {
					ctx = WithSafeDiagnostics(ctx)
					data["err"], data["sql"] = diagnosticsError{hostile}, diagnosticsStringer{hostile}
					data["args"] = []any{hostile}
					for _, key := range []string{"host", "port", "database", "DSN", "pid", "table", "column", "name", "commandTag", "rowcount", "unknown", "password", "secret", "token", "Message", "Detail", "Hint", "Where"} {
						data[key] = hostile
					}
				}
				output.Reset()
				(&stdLogger{}).Log(ctx, level, "Query", data)
				logged := output.String()
				requireDiagnosticsUnformatted(t, hostile)
				if level == tracelog.LogLevelTrace || level == tracelog.LogLevelNone {
					require.Empty(t, logged)
					continue
				}
				require.NotEmpty(t, logged)
				if safe {
					payload := "[" + strings.ToUpper(level.String()) + "] Database event=Query class=other duration=1ms\n"
					require.Regexp(t, `^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `+regexp.QuoteMeta(payload)+`$`, logged)
					for _, value := range []string{"raw-sql", "raw-key", "raw-hash", "raw-error", "forbidden-", "map[", "sql:", "args:", "err:", "host:", "pid:", "Detail:"} {
						require.NotContains(t, logged, value)
					}
					output.Reset()
					(&stdLogger{}).Log(ctx, level, "Query", map[string]any{"err": &pgconn.PgError{Code: "P0001", Message: "forbidden-message", Detail: "forbidden-detail"}, "time": time.Millisecond, "args": hostile})
					payload = "[" + strings.ToUpper(level.String()) + "] Database event=Query class=database sqlstate=P0001 duration=1ms\n"
					require.Regexp(t, `^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `+regexp.QuoteMeta(payload)+`$`, output.String())
					require.NotContains(t, output.String(), "forbidden-")
					requireDiagnosticsUnformatted(t, hostile)
				} else if level != tracelog.LogLevelInfo {
					require.Contains(t, logged, "raw-sql")
					require.Contains(t, logged, "raw-error")
					require.Equal(t, bind == "true", bytes.Contains(output.Bytes(), []byte("raw-key")))
				} else {
					require.Contains(t, logged, "Query")
					require.NotContains(t, logged, "raw-")
				}
			}
		}
	}
}

func TestAPIDatabaseDiagnosticsUsageProjection(t *testing.T) {
	hostile := &diagnosticsHostile{}
	var nilPG *pgconn.PgError
	var nilConnect *pgconn.ConnectError
	for _, tc := range []struct {
		err         any
		class, code string
	}{
		{nil, "ok", ""}, {hostile, "other", ""}, {"raw", "other", ""},
		{nilPG, "other", ""}, {nilConnect, "other", ""},
		{fmt.Errorf("wrapped: %w", context.Canceled), "canceled", ""},
		{fmt.Errorf("wrapped: %w", context.DeadlineExceeded), "deadline", ""},
		{&pgconn.ConnectError{}, "connection", ""},
		{fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "P0001", Message: "raw", Detail: "raw", Hint: "raw", Where: "raw"}), "database", "P0001"},
	} {
		d := projectDiagnostic("raw-event", map[string]any{"err": tc.err})
		require.Equal(t, databaseDiagnostic{event: "Database", class: tc.class, sqlState: tc.code}, d)
	}
	for _, code := range []string{"", "p0001", "123456", "12\n34", "Ａ0001"} {
		require.Empty(t, projectDiagnostic("Query", map[string]any{"err": &pgconn.PgError{Code: code}}).sqlState)
	}
	for _, duration := range []any{nil, -time.Nanosecond, int64(1), "1ms", hostile} {
		require.False(t, projectDiagnostic("Query", map[string]any{"time": duration}).hasElapsed)
	}
	for _, duration := range []time.Duration{0, time.Nanosecond, time.Duration(1<<63 - 1)} {
		d := projectDiagnostic("Query", map[string]any{"time": duration})
		require.True(t, d.hasElapsed)
		require.Equal(t, duration, d.elapsed)
	}
	for _, event := range []string{"Query", "BatchQuery", "BatchClose", "CopyFrom", "Connect", "Prepare", "Acquire", "Release"} {
		require.Equal(t, event, projectDiagnostic(event, nil).event)
	}
	requireDiagnosticsUnformatted(t, hostile)
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), diagnosticsContextKey{}, "retained"), time.Hour)
	defer cancel()
	marked := WithSafeDiagnostics(ctx)
	require.Same(t, marked, WithSafeDiagnostics(marked))
	require.Equal(t, "retained", marked.Value(diagnosticsContextKey{}))
	want, _ := ctx.Deadline()
	got, ok := marked.Deadline()
	require.True(t, ok)
	require.Equal(t, want, got)
	require.Equal(t, ctx.Done(), marked.Done())
	cancel()
	require.ErrorIs(t, marked.Err(), context.Canceled)
}
