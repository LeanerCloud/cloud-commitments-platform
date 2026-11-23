package server

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/exchange"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// configExchangeStoreAdapter adapts config.StoreInterface to exchange.RIExchangeStore.
// It converts between config.RIExchangeRecord and exchange.ExchangeRecord.
type configExchangeStoreAdapter struct {
	store config.StoreInterface
}

func newConfigExchangeStoreAdapter(store config.StoreInterface) *configExchangeStoreAdapter {
	return &configExchangeStoreAdapter{store: store}
}

// awsAccountIDPattern is the shape of an AWS account ID: exactly 12 ASCII digits
// (RE2's \d is ASCII-only).
var awsAccountIDPattern = regexp.MustCompile(`^\d{12}$`)

// requireAWSExchangeAccountID refuses an exchange record whose account ID is not
// an AWS account ID. Every record this adapter writes comes from the AWS EC2
// exchange engine, so its account ID must be the 12-digit account STS
// reported. ri_exchange_history.account_id itself no longer enforces that
// shape (migration 000137 dropped the AWS-only CHECK so the column can hold
// Azure and GCP identifiers, issue #144); this check keeps the guarantee for
// AWS rows at the boundary where the provider is known, including refusing the
// "unknown" placeholder resolveAccountID falls back to when STS fails.
func requireAWSExchangeAccountID(accountID string) error {
	if !awsAccountIDPattern.MatchString(accountID) {
		return fmt.Errorf("refusing to record RI exchange: account ID %q is not a 12-digit AWS account ID", accountID)
	}
	return nil
}

func (a *configExchangeStoreAdapter) SaveRIExchangeRecord(ctx context.Context, record *exchange.ExchangeRecord) error {
	if err := requireAWSExchangeAccountID(record.AccountID); err != nil {
		return err
	}
	cfgRecord := exchangeToConfigRecord(record)
	if err := a.store.SaveRIExchangeRecord(ctx, cfgRecord); err != nil {
		return err
	}
	record.ID = cfgRecord.ID
	return nil
}

func (a *configExchangeStoreAdapter) ReserveRIExchange(ctx context.Context, record *exchange.ExchangeRecord, dailyCapUSD, perExchangeCapUSD string) (string, error) {
	if err := requireAWSExchangeAccountID(record.AccountID); err != nil {
		return "", err
	}
	cfgRecord := exchangeToConfigRecord(record)
	ceiling, err := a.store.ReserveRIExchange(ctx, cfgRecord, dailyCapUSD, perExchangeCapUSD)
	if err != nil {
		return "", err
	}
	record.ID = cfgRecord.ID
	record.PaymentDue = cfgRecord.PaymentDue
	return ceiling, nil
}

func (a *configExchangeStoreAdapter) CancelAllPendingExchanges(ctx context.Context) (int64, error) {
	return a.store.CancelAllPendingExchanges(ctx)
}

func (a *configExchangeStoreAdapter) CancelPendingExchangesByOrigin(ctx context.Context, origin common.ExchangeOrigin) (int64, error) {
	return a.store.CancelPendingExchangesByOrigin(ctx, origin)
}

func (a *configExchangeStoreAdapter) GetStaleProcessingExchanges(ctx context.Context, olderThan time.Duration) ([]exchange.ExchangeRecord, error) {
	cfgRecords, err := a.store.GetStaleProcessingExchanges(ctx, olderThan)
	if err != nil {
		return nil, err
	}
	result := make([]exchange.ExchangeRecord, len(cfgRecords))
	for i := range cfgRecords {
		r := cfgRecords[i]
		result[i] = configToExchangeRecord(&r)
	}
	return result, nil
}

func (a *configExchangeStoreAdapter) GetRIExchangeDailySpend(ctx context.Context, date time.Time) (string, error) {
	return a.store.GetRIExchangeDailySpend(ctx, date)
}

func (a *configExchangeStoreAdapter) CompleteRIExchange(ctx context.Context, id, exchangeID string) error {
	return a.store.CompleteRIExchange(ctx, id, exchangeID)
}

func (a *configExchangeStoreAdapter) CompleteRIExchangeWithPayment(ctx context.Context, id, exchangeID, acceptedPaymentDue string) error {
	return a.store.CompleteRIExchangeWithPayment(ctx, id, exchangeID, acceptedPaymentDue)
}

func (a *configExchangeStoreAdapter) FailRIExchange(ctx context.Context, id, errorMsg string) error {
	return a.store.FailRIExchange(ctx, id, errorMsg)
}

// exchangeToConfigRecord leaves CreatedByUserID nil on purpose: these rows are
// written by the scheduler or the ladder engine, which have no human creator,
// and pkg/exchange.ExchangeRecord has no field to carry one (issue #518).
func exchangeToConfigRecord(r *exchange.ExchangeRecord) *config.RIExchangeRecord {
	return &config.RIExchangeRecord{
		ID:                 r.ID,
		AccountID:          r.AccountID,
		ExchangeID:         r.ExchangeID,
		Region:             r.Region,
		SourceRIIDs:        r.SourceRIIDs,
		SourceInstanceType: r.SourceInstanceType,
		SourceCount:        r.SourceCount,
		TargetOfferingID:   r.TargetOfferingID,
		TargetInstanceType: r.TargetInstanceType,
		TargetCount:        r.TargetCount,
		PaymentDue:         r.PaymentDue,
		Status:             r.Status,
		ApprovalToken:      r.ApprovalToken,
		Error:              r.Error,
		Mode:               r.Mode,
		LadderRunID:        r.LadderRunID,
		CreatedAt:          r.CreatedAt,
		UpdatedAt:          r.UpdatedAt,
		CompletedAt:        r.CompletedAt,
		ExpiresAt:          r.ExpiresAt,
	}
}

func configToExchangeRecord(r *config.RIExchangeRecord) exchange.ExchangeRecord {
	return exchange.ExchangeRecord{
		ID:                 r.ID,
		AccountID:          r.AccountID,
		ExchangeID:         r.ExchangeID,
		Region:             r.Region,
		SourceRIIDs:        r.SourceRIIDs,
		SourceInstanceType: r.SourceInstanceType,
		SourceCount:        r.SourceCount,
		TargetOfferingID:   r.TargetOfferingID,
		TargetInstanceType: r.TargetInstanceType,
		TargetCount:        r.TargetCount,
		PaymentDue:         r.PaymentDue,
		Status:             r.Status,
		ApprovalToken:      r.ApprovalToken,
		Error:              r.Error,
		Mode:               r.Mode,
		LadderRunID:        r.LadderRunID,
		CreatedAt:          r.CreatedAt,
		UpdatedAt:          r.UpdatedAt,
		CompletedAt:        r.CompletedAt,
		ExpiresAt:          r.ExpiresAt,
	}
}
