package server

import (
	"context"
	"fmt"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/commitmentopts"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/credentials"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/oidc"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// decorateSenderWithMute wires per-recipient mute suppression and the
// List-Unsubscribe base URL into the concrete SES (*email.Sender) or SMTP
// (*email.SMTPSender) sender. Other implementations (e.g. the no-op sender used
// when EMAIL_ENABLED=false) are returned unchanged. configStore satisfies
// email.MuteChecker via its IsNotificationMuted method. dashboardURL is the
// already-trimmed dashboard base URL; an empty value disables the
// List-Unsubscribe header (the mute checker is still wired in).
func decorateSenderWithMute(sender email.SenderInterface, mc email.MuteChecker, dashboardURL string) email.SenderInterface {
	switch s := sender.(type) {
	case *email.Sender:
		return s.WithMuteChecker(mc).WithUnsubscribeBaseURL(dashboardURL)
	case *email.SMTPSender:
		return s.WithMuteChecker(mc).WithUnsubscribeBaseURL(dashboardURL)
	default:
		return sender
	}
}

// initAuthService derives a stable CSRF key from encKey via HKDF and
// constructs the auth.Service. Extracted from reinitializeAfterConnect to keep
// cyclomatic complexity within the project limit. Returns an error if CSRF key
// derivation fails or if NewService returns nil (fail-closed).
func (app *Application) initAuthService(authStore *auth.PostgresStore, encKey []byte) (*auth.Service, error) {
	// Derive a STABLE CSRF key from the encryption key so every instance and
	// every Lambda cold-start uses the same key (closes the cross-instance CSRF
	// failure: a token minted on one instance must validate on another). Without
	// an explicit CSRFKey, auth.NewService would mint a random per-process key,
	// invalidating tokens across the fleet and on every cold-start.
	csrfKey, err := auth.DeriveCSRFKey(encKey)
	if err != nil {
		return nil, fmt.Errorf("failed to derive CSRF key: %w", err)
	}
	svc := auth.NewService(auth.ServiceConfig{
		Store:            authStore,
		EmailSender:      app.Email,
		SessionDuration:  24 * time.Hour,
		DashboardURL:     app.appConfig.DashboardURL,
		OnPasswordChange: buildAdminPasswordSyncCallback(authStore, app.secretResolver),
		CSRFKey:          csrfKey,
		BcryptCost:       app.appConfig.authBcryptCost,
	})
	if svc == nil {
		return nil, fmt.Errorf("failed to create auth service")
	}
	return svc, nil
}

type cleanupWorkerStarter interface {
	StartCleanupWorker(ctx context.Context)
}

// startRateLimiterCleanup runs the limiter's scheduled cleanup on the
// process-lifetime context (canceled by Close), not the request context that
// triggered the lazy DB connect.
func (app *Application) startRateLimiterCleanup(rl cleanupWorkerStarter) {
	app.lifeMu.Lock()
	defer app.lifeMu.Unlock()
	if app.lifeCtx == nil {
		app.lifeCtx, app.lifeCancel = context.WithCancel(context.Background())
	}
	if app.rlCleanupStop != nil {
		app.rlCleanupStop()
	}
	var workerCtx context.Context
	workerCtx, app.rlCleanupStop = context.WithCancel(app.lifeCtx)
	rl.StartCleanupWorker(workerCtx)
}

// postDBPurchaseManagerConfig is the purchase Manager config once the database
// is connected. pgStore is the attempt journal (cloud-commitments-go#107); the
// pre-DB Manager leaves it nil on purpose and is replaced by this one.
func postDBPurchaseManagerConfig(app *Application, pgStore *config.PostgresStore, credStore credentials.CredentialStore, awsCfg aws.Config) purchase.ManagerConfig {
	return purchase.ManagerConfig{
		ConfigStore:            app.Config,
		EmailSender:            app.Email,
		STSClient:              sts.NewFromConfig(awsCfg),
		AssumeRoleSTS:          sts.NewFromConfig(awsCfg),
		BastionSTSFactory:      credentials.NewSTSClientFactory(awsCfg),
		AmbientAWSCreds:        awsCfg.Credentials,
		CredentialStore:        credStore,
		AttemptJournal:         pgStore,
		NotificationDaysBefore: app.appConfig.NotificationDaysBefore,
		MaxDueRowAge:           app.appConfig.ScheduledPurchaseMaxOverdue,
		DashboardURL:           app.appConfig.DashboardURL,
		OIDCSigner:             app.signer,
		OIDCIssuerURL:          resolveOIDCIssuerURL(app.appConfig),
	}
}

// hostAndAssumeRoleSTS is the STS surface the probe config builder needs:
// AssumeRole for role-based accounts and GetCallerIdentity for the
// host-identity check on role_arn accounts with no role ARN.
type hostAndAssumeRoleSTS interface {
	credentials.STSClient
	credentials.CallerIdentityClient
}

// newCommitmentProbeConfigBuilder resolves the aws.Config the commitment-options
// probe uses for an account. hostCfg's credentials and stsClient let a role_arn
// account with no role ARN use the host credentials only when it is the host
// account. accounts and hostCfg let a bastion account be reached through its
// bastion's own credentials (issue #263).
func newCommitmentProbeConfigBuilder(credStore credentials.CredentialStore, accounts credentials.CloudAccountGetter, hostCfg aws.Config, stsClient hostAndAssumeRoleSTS) commitmentopts.BuildConfigFn {
	opts := credentials.AWSResolveOptions{
		AccountLookup:    credentials.AccountLookupFromStore(accounts),
		STSClientFactory: credentials.NewSTSClientFactory(hostCfg),
		AmbientProvider:  hostCfg.Credentials,
		AmbientSTS:       stsClient,
	}
	return func(ctx context.Context, acct *config.CloudAccount) (aws.Config, error) {
		prov, err := credentials.ResolveAWSCredentialProviderWithOpts(ctx, acct, credStore, stsClient, opts)
		if err != nil {
			return aws.Config{}, err
		}
		// us-east-1 hardcoded because reserved offerings are global
		// facts (not AZ-scoped), and us-east-1 has the widest
		// instance-type coverage. This fails silently for GovCloud
		// / China-partition accounts - those return ErrNoData from
		// the probe and the frontend falls back to hardcoded rules,
		// which is acceptable since those partitions rarely need
		// dynamic commitment detection.
		return awsconfig.LoadDefaultConfig(ctx,
			awsconfig.WithCredentialsProvider(prov),
			awsconfig.WithRegion("us-east-1"),
		)
	}
}

// newCommitmentProbeAzureCredential resolves the token credential the Azure
// commitment-options probe uses for an account, the same way the scheduler does.
func newCommitmentProbeAzureCredential(credStore credentials.CredentialStore, signer oidc.Signer, issuerURL string) commitmentopts.AzureCredentialFn {
	return func(ctx context.Context, acct *config.CloudAccount) (azcore.TokenCredential, error) {
		return credentials.ResolveAzureTokenCredentialWithOpts(ctx, acct, credStore, credentials.AzureResolveOptions{
			Signer:    signer,
			IssuerURL: issuerURL,
		})
	}
}

// azureProbeCurrency is the billing currency the Azure probe validates in. The
// cloud account has no currency field and the pinned cloud-commitments-go
// Azure Savings Plans provider only collects and buys USD (it rejects any other
// recommendation currency), so USD is the one currency the platform supports
// for Azure. A per-account billing-currency lookup is the place to extend this.
func azureProbeCurrency(context.Context, *config.CloudAccount) (string, error) {
	return "USD", nil
}
