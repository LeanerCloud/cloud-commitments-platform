package commitmentopts

import (
	"context"
	"fmt"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// providerAzure keys the Azure probe run row.
const providerAzure = "azure"

// AzureCredentialFn resolves the token credential for an Azure account.
type AzureCredentialFn func(ctx context.Context, account *config.CloudAccount) (azcore.TokenCredential, error)

// AzureCurrencyFn returns the billing currency (3-letter ISO 4217 code) of an
// Azure account. It must fail rather than guess: Azure validates a purchase
// in the billing account's currency.
type AzureCurrencyFn func(ctx context.Context, account *config.CloudAccount) (string, error)

const (
	// azureRefreshTimeout bounds one RefreshAzure run: up to six sequential
	// ValidatePurchase calls plus credential resolution.
	azureRefreshTimeout = 60 * time.Second
	// azureFailureTTL is how long a failed RefreshAzure is remembered, so
	// repeated triggers cannot hammer Azure with a probe that keeps failing.
	azureFailureTTL = 10 * time.Minute
)

// azureProbe is the optional Azure half of a Service. A Service without it
// never probes Azure.
type azureProbe struct {
	now         func() time.Time
	credential  AzureCredentialFn
	currency    AzureCurrencyFn
	lastErr     error
	failedUntil time.Time
	probers     []*AzureSPProber
	timeout     time.Duration
}

// WithAzure enables the Azure probe. Call it once, before the Service is
// shared between goroutines. It only makes RefreshAzure available: Get never
// touches Azure.
func (s *Service) WithAzure(credential AzureCredentialFn, currency AzureCurrencyFn, probers []*AzureSPProber) *Service {
	s.azure = &azureProbe{now: time.Now, timeout: azureRefreshTimeout, credential: credential, currency: currency, probers: probers}
	return s
}

// RefreshAzure probes Azure and persists the azure probe run row unless one
// exists already. It is a no-op without WithAzure or without an enabled Azure
// account. It is deliberately not part of Get, so AWS requests never wait on
// Azure; nothing calls it yet. The API handler or background trigger that
// consumes Azure options (slice 4 of #17) is its caller.
//
// The run is bounded by azureRefreshTimeout. A failure is remembered for
// azureFailureTTL and returned again, without any Azure call, until it
// expires; a failure caused by the caller canceling its own context is not.
// A failure persists nothing and never touches the AWS row.
func (s *Service) RefreshAzure(ctx context.Context) error {
	if s.azure == nil {
		return nil
	}
	has, err := s.store.HasData(ctx, providerAzure)
	if err != nil {
		return fmt.Errorf("read commitmentopts store: %w", err)
	}
	if has {
		return nil
	}

	s.azureMu.Lock()
	defer s.azureMu.Unlock()

	if has, err = s.store.HasData(ctx, providerAzure); err != nil {
		return fmt.Errorf("read commitmentopts store: %w", err)
	} else if has {
		return nil
	}
	if s.azure.lastErr != nil && s.azure.now().Before(s.azure.failedUntil) {
		return s.azure.lastErr
	}

	probeCtx, cancel := context.WithTimeout(ctx, s.azure.timeout)
	defer cancel()
	if err := s.probeAndPersistAzure(probeCtx); err != nil {
		logging.Warnf("commitmentopts: azure probe failed: %v", err)
		if ctx.Err() != nil {
			// The caller gave up; that says nothing about Azure, so the next
			// caller must not inherit it. Our own timeout expiring is recorded.
			return err
		}
		s.azure.lastErr = err
		s.azure.failedUntil = s.azure.now().Add(azureFailureTTL)
		return err
	}
	return nil
}

// probeAndPersistAzure probes the first enabled Azure account and saves the
// result under the azure provider row. No enabled account is a clean no-op.
// Any failure persists nothing, so the next call retries.
func (s *Service) probeAndPersistAzure(ctx context.Context) error {
	account, err := s.findEnabledAccount(ctx, providerAzure)
	if err != nil {
		return err
	}
	if account == nil {
		return nil
	}
	if account.AzureSubscriptionID == "" {
		return fmt.Errorf("azure account %s has no azure_subscription_id", account.ID)
	}

	cred, err := s.azure.credential(ctx, account)
	if err != nil {
		return fmt.Errorf("azure account %s credential: %w", account.ID, err)
	}
	currency, err := s.azure.currency(ctx, account)
	if err != nil {
		return fmt.Errorf("azure account %s currency: %w", account.ID, err)
	}

	var all []Combo
	for _, p := range s.azure.probers {
		combos, err := p.ProbeAzure(ctx, cred, account.AzureSubscriptionID, currency)
		if err != nil {
			return fmt.Errorf("azure account %s probe %s: %w", account.ID, p.Service(), err)
		}
		all = append(all, combos...)
	}

	if err := s.store.Save(ctx, providerAzure, all, account.ExternalID); err != nil {
		return fmt.Errorf("persist azure commitment options: %w", err)
	}
	return nil
}
