// Package main provides the unified entry point for CUDly server.
// It supports both AWS Lambda and standard HTTP server modes.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/runtime"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/server"
)

// Version, BuildTime, and GitSHA are set at build time via ldflags.
var (
	Version   = "dev"
	BuildTime = "unknown"
	GitSHA    = "unknown"
)

func main() {
	os.Exit(realMain(os.Args[1:]))
}

// handlers are the three things the process can do; dispatch picks one.
type handlers struct {
	migrate func(cliFlags) int
	task    func(cliFlags) int
	serve   func(cliFlags) int
}

// dispatch runs exactly one handler. --migrate comes first so it never builds
// the full Application (email sender, STS, app config); --task precedes the
// listener start.
func dispatch(c cliFlags, h handlers) int {
	switch {
	case c.migrate:
		return h.migrate(c)
	case c.task != "":
		return h.task(c)
	default:
		return h.serve(c)
	}
}

func realMain(args []string) int {
	cli, err := parseFlags(args)
	if err != nil {
		return flagErrorCode(err)
	}

	log.Printf("CUDly Server v%s (git: %s, built: %s)", Version, GitSHA, BuildTime)

	// Export BUILD_TIME and GIT_SHA to the environment so the api package can
	// read them without importing main (import cycle). VERSION is passed
	// directly to NewApplication to avoid the env round-trip (04-N1).
	for k, v := range map[string]string{"BUILD_TIME": BuildTime, "GIT_SHA": GitSHA} {
		if envErr := os.Setenv(k, v); envErr != nil {
			log.Printf("failed to export %s: %v", k, envErr)
		}
	}

	return dispatch(cli, handlers{
		migrate: func(c cliFlags) int {
			return runMigrateMode(context.Background(), c.migrateOptions(), migrateOnce)
		},
		task:  runTaskMode,
		serve: runServeMode,
	})
}

// flagErrorCode maps a flag error to the exit code flag.ExitOnError would use:
// 0 for -h, 2 for anything else.
func flagErrorCode(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	log.Printf("Invalid flags: %v", err)
	return 2
}

func runTaskMode(c cliFlags) int {
	ctx := context.Background()
	app, err := server.NewApplication(ctx, Version)
	if err != nil {
		log.Printf("Failed to initialize application: %v", err)
		return 1
	}
	defer app.Close()

	timeout := getTaskTimeout()
	taskCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	log.Printf("Running scheduled task: %s (timeout: %v)", c.task, timeout)
	result, err := app.HandleScheduledTask(taskCtx, server.ScheduledTaskType(c.task), server.ScheduledTaskParams{})
	if err != nil {
		log.Printf("Scheduled task %q failed: %v", c.task, err)
		return 1
	}
	log.Printf("Scheduled task %q completed successfully: %v", c.task, result)
	return 0
}

func runServeMode(c cliFlags) int {
	// Pass Version directly so it is stamped on ApplicationConfig without
	// going through os.Setenv("VERSION",...).
	app, err := server.NewApplication(context.Background(), Version)
	if err != nil {
		log.Printf("Failed to initialize application: %v", err)
		return 1
	}
	defer app.Close()

	runtimeMode := determineRuntimeMode(c.mode)
	log.Printf("Starting CUDly server in %s mode", runtimeMode)

	switch runtimeMode {
	case "lambda":
		server.StartLambdaHandler(app)
	case "http":
		if err := server.StartHTTPServer(app, c.port); err != nil {
			log.Printf("HTTP server failed: %v", err)
			return 1
		}
	default:
		log.Printf("Unknown runtime mode: %s", runtimeMode)
		return 1
	}
	return 0
}

type cliFlags struct {
	mode         string
	task         string
	port         int
	forceVersion int
	forceSet     bool
	migrate      bool
}

// parseFlags parses args. The trailing --mode/--port that
// scripts/entrypoint.sh appends to a command override are valid flags and
// simply unused in migrate mode.
func parseFlags(args []string) (cliFlags, error) {
	var c cliFlags
	fs := flag.NewFlagSet("cudly", flag.ContinueOnError)
	fs.StringVar(&c.mode, "mode", "auto", "Runtime mode: auto, lambda, http")
	fs.IntVar(&c.port, "port", 8080, "HTTP server port (ignored in lambda mode)")
	fs.StringVar(&c.task, "task", "", "Run a scheduled task and exit (e.g., collect_recommendations, cleanup)")
	fs.BoolVar(&c.migrate, "migrate", false, "Apply pending database migrations and exit")
	fs.IntVar(&c.forceVersion, "migrate-force-version", -1, "With --migrate: clear a dirty migration row at this version (dirty version N or N-1 only)")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	fs.Visit(func(f *flag.Flag) { c.forceSet = c.forceSet || f.Name == "migrate-force-version" })
	return c, c.validate()
}

// validate fails loud on combinations that would otherwise silently drop an
// operator's intent.
func (c cliFlags) validate() error {
	if c.forceSet && c.forceVersion < 0 {
		return errors.New("--migrate-force-version must be a non-negative integer")
	}
	if c.forceSet && !c.migrate {
		return errors.New("--migrate-force-version requires --migrate")
	}
	if c.migrate && c.task != "" {
		return errors.New("--migrate and --task are mutually exclusive")
	}
	return nil
}

func (c cliFlags) migrateOptions() server.MigrateOptions {
	opts := server.MigrateOptions{Timeout: getTaskTimeout()}
	if c.forceSet {
		opts.ForceVersion = &c.forceVersion
	}
	return opts
}

// migrateOnce loads the DB config and runs the deploy migrator.
func migrateOnce(ctx context.Context, opts server.MigrateOptions) (server.MigrateReport, error) {
	dbCfg, resolver, err := server.InitConfigStore(ctx)
	if err != nil {
		return server.MigrateReport{}, err
	}
	return server.RunDeployMigrations(ctx, dbCfg, resolver, opts)
}

// runMigrateMode runs migrate and returns the process exit code.
func runMigrateMode(ctx context.Context, opts server.MigrateOptions, migrate func(context.Context, server.MigrateOptions) (server.MigrateReport, error)) int {
	rep, err := migrate(ctx, opts)
	if err != nil {
		log.Printf("Migration failed: %v", err)
		return 1
	}
	log.Printf("Migrations complete: version %d -> %d", rep.From, rep.To)
	return 0
}

// getTaskTimeout returns the task timeout from TASK_TIMEOUT env var or the default of 15 minutes.
// Logs a warning when TASK_TIMEOUT is set but cannot be parsed or is non-positive,
// so the operator knows the value was not applied.
func getTaskTimeout() time.Duration {
	const defaultTimeout = 15 * time.Minute
	if v := os.Getenv("TASK_TIMEOUT"); v != "" {
		secs, err := strconv.Atoi(v)
		if err != nil {
			log.Printf("WARNING: TASK_TIMEOUT=%q is not a valid integer; using default %v", v, defaultTimeout) // #nosec G706 -- TASK_TIMEOUT env var is operator-controlled; logged for diagnostics
			return defaultTimeout
		}
		if secs <= 0 {
			log.Printf("WARNING: TASK_TIMEOUT=%q must be a positive number; using default %v", v, defaultTimeout) // #nosec G706 -- TASK_TIMEOUT env var is operator-controlled; logged for diagnostics
			return defaultTimeout
		}
		return time.Duration(secs) * time.Second
	}
	return defaultTimeout
}

// determineRuntimeMode determines the runtime mode based on flags and environment.
//
// Precedence: the --mode flag, then an explicit RUNTIME_MODE, then Lambda
// auto-detection. Explicit configuration outranks ambient detection (issue
// #111): with RUNTIME_MODE=http under the Lambda Runtime Interface Emulator,
// which sets AWS_LAMBDA_RUNTIME_API, the operator gets the HTTP server they
// asked for rather than a silent Lambda start. RUNTIME_MODE=auto (the
// Dockerfile default) or an empty value means auto-detect.
func determineRuntimeMode(modeFlag string) string {
	// If mode is explicitly set, use it
	if modeFlag != "auto" {
		return modeFlag
	}

	// Check for explicit RUNTIME_MODE environment variable
	switch runtimeMode := os.Getenv("RUNTIME_MODE"); runtimeMode {
	case "lambda", "http":
		return runtimeMode
	case "", "auto":
		// Not set, or explicitly auto: fall through to detection below.
	default:
		log.Printf("Warning: unrecognized RUNTIME_MODE %q, falling back to auto-detection", runtimeMode) // #nosec G706 -- RUNTIME_MODE env var is operator-controlled; logged for diagnostics
	}

	// Auto-detect based on environment using the canonical runtime helper,
	// which encapsulates the detection rule so future changes stay consistent
	// across all call sites (issue 04-M5).
	if runtime.IsLambda() {
		return "lambda"
	}

	// Default to HTTP mode for containers (Fargate, Cloud Run, Container Apps)
	return "http"
}
