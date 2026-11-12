package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// HealthStatus represents the overall health of the application.
type HealthStatus struct {
	Timestamp time.Time              `json:"timestamp"`
	Checks    map[string]CheckResult `json:"checks"`
	Status    string                 `json:"status"`
	Version   string                 `json:"version"`
}

// CheckResult represents the result of a health check.
type CheckResult struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// handleHealthCheck returns the health status of the application.
func (app *Application) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	app.writeHealth(w, r, false)
}

func (app *Application) handleReadinessCheck(w http.ResponseWriter, r *http.Request) {
	app.writeHealth(w, r, true)
}

func (app *Application) writeHealth(w http.ResponseWriter, r *http.Request, readiness bool) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	health := app.healthStatus(ctx)
	status := http.StatusOK
	if readiness && health.Status != "healthy" {
		status = http.StatusServiceUnavailable
	}
	setHealthResponseHeaders(w, app.appConfig.CORSAllowedOrigin)
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(health); err != nil {
		log.Printf("health: failed to encode response: %v", err)
	}
}

func (app *Application) healthStatus(ctx context.Context) HealthStatus {
	health := HealthStatus{
		Status:    "healthy",
		Version:   app.Version,
		Timestamp: time.Now(),
		Checks:    make(map[string]CheckResult),
	}

	snapshot := app.healthSnapshot()
	if snapshot == nil {
		health.Checks["config_store"] = CheckResult{Status: "pending", Message: "Application initialization has not completed"}
		health.Checks["auth_store"] = CheckResult{Status: "pending", Message: "Application initialization has not completed"}
	} else {
		health.Checks["config_store"] = snapshot.checkConfigStore(ctx)
		health.Checks["auth_store"] = snapshot.checkAuthStore(ctx)
		if snapshot.dbConfig != nil && !snapshot.dbConnected {
			health.Status = "degraded"
		}
	}
	if health.Checks["config_store"].Status != "healthy" {
		health.Status = "degraded"
	}

	if health.Checks["auth_store"].Status != "healthy" {
		health.Status = "degraded"
	}

	health.Checks["migrations"] = app.checkMigrations()
	switch health.Checks["migrations"].Status {
	case "failed", "pending":
		health.Status = "degraded"
	}

	return health
}

func (app *Application) healthSnapshot() *Application {
	if app.httpInit != nil {
		select {
		case <-app.httpInit.done:
			if app.httpInit.err != nil {
				return nil
			}
		default:
			return nil
		}
	} else {
		if !app.dbMu.TryLock() {
			return nil
		}
		defer app.dbMu.Unlock()
	}
	// Copy only health dependencies, never Application's mutexes; ping after releasing dbMu.
	return &Application{
		Config: app.Config, Auth: app.Auth, DB: app.DB,
		dbConfig: app.dbConfig, dbConnected: app.dbConnected,
	}
}

// checkMigrations reports the outcome of the most recent migration run.
// disabled = AutoMigrate is off; migrations happen elsewhere (e.g. CI)
// pending  = AutoMigrate is on but ensureDB hasn't completed yet
// failed   = last attempt returned an error OR timed out
// healthy  = last attempt completed without error.
func (app *Application) checkMigrations() CheckResult {
	// No dbConfig means the app isn't using PostgreSQL at all (DynamoDB or
	// test mode). AutoMigrate off means migrations are handled elsewhere
	// (e.g. a dedicated CI deploy step). Either way, "disabled" correctly
	// reports that this health facet is not applicable — the overall
	// status stays healthy.
	if app.dbConfig == nil || !app.dbConfig.AutoMigrate {
		return CheckResult{Status: "disabled", Message: "AutoMigrate is off"}
	}
	err, finishedAt := app.snapshotMigrationState()
	switch {
	case finishedAt.IsZero():
		return CheckResult{Status: "pending", Message: "migrations have not run yet"}
	case err != nil:
		return CheckResult{Status: "failed", Message: err.Error()}
	default:
		return CheckResult{
			Status:  "healthy",
			Message: fmt.Sprintf("last run %s ago", time.Since(finishedAt).Truncate(time.Second)),
		}
	}
}

// checkConfigStore checks the health of the configuration store.
func (app *Application) checkConfigStore(ctx context.Context) CheckResult {
	// Check if config store exists
	if app.Config == nil {
		// If using PostgreSQL with lazy initialization, DB might not be connected yet
		if app.dbConfig != nil {
			return CheckResult{
				Status:  "pending",
				Message: "Database connection pending (lazy initialization)",
			}
		}
		return CheckResult{
			Status:  "unhealthy",
			Message: "Config store not initialized",
		}
	}

	// If using PostgreSQL, check database connection health
	if app.DB != nil {
		if err := app.DB.HealthCheck(ctx); err != nil {
			return CheckResult{
				Status:  "unhealthy",
				Message: fmt.Sprintf("Database health check failed: %v", err),
			}
		}
	}

	return CheckResult{
		Status: "healthy",
	}
}

// setHealthResponseHeaders adds security headers and CORS to the health endpoint response.
// These match the headers set by internal/api/handler.go for API responses, ensuring
// consistent security posture when hitting the container directly without a CDN.
func setHealthResponseHeaders(w http.ResponseWriter, corsOrigin string) {
	w.Header().Set("Content-Type", "application/json")

	// Security headers
	w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-XSS-Protection", "1; mode=block")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")

	// CORS headers
	if corsOrigin != "" {
		w.Header().Set("Access-Control-Allow-Origin", corsOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key, Authorization, X-Authorization, X-CSRF-Token")
	}
}

// checkAuthStore checks the health of the auth store.
func (app *Application) checkAuthStore(ctx context.Context) CheckResult {
	if app.Auth == nil {
		return CheckResult{
			Status:  "unhealthy",
			Message: "Auth service not initialized",
		}
	}

	// Ping the database to verify connection is healthy
	if err := app.Auth.Ping(ctx); err != nil {
		return CheckResult{
			Status:  "unhealthy",
			Message: fmt.Sprintf("Auth store ping failed: %v", err),
		}
	}

	return CheckResult{
		Status: "healthy",
	}
}
