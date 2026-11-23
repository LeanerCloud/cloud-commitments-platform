package mocks

import (
	"context"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// SavePurchaseHistory mocks the SavePurchaseHistory operation.
func (m *MockConfigStore) SavePurchaseHistory(ctx context.Context, record *config.PurchaseHistoryRecord) error {
	m.record("SavePurchaseHistory", ctx, record)
	args := m.Called(ctx, record)
	return args.Error(0)
}

// GetPurchaseHistory mocks the GetPurchaseHistory operation.
func (m *MockConfigStore) GetPurchaseHistory(ctx context.Context, accountID string, limit int) ([]config.PurchaseHistoryRecord, error) {
	m.record("GetPurchaseHistory", ctx, accountID, limit)
	args := m.Called(ctx, accountID, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseHistoryRecord)
	if !ok {
		m.reporter.wrongReturnType("GetPurchaseHistory", "[]config.PurchaseHistoryRecord", args.Get(0))
	}
	return v, args.Error(1)
}

// GetAllPurchaseHistory mocks the GetAllPurchaseHistory operation.
func (m *MockConfigStore) GetAllPurchaseHistory(ctx context.Context, limit int) ([]config.PurchaseHistoryRecord, error) {
	m.record("GetAllPurchaseHistory", ctx, limit)
	args := m.Called(ctx, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseHistoryRecord)
	if !ok {
		m.reporter.wrongReturnType("GetAllPurchaseHistory", "[]config.PurchaseHistoryRecord", args.Get(0))
	}
	return v, args.Error(1)
}

// GetActivePurchaseHistory mocks the GetActivePurchaseHistory operation.
func (m *MockConfigStore) GetActivePurchaseHistory(ctx context.Context, asOf time.Time, accountIDs []string, externalIDsByProvider map[string][]string) ([]config.PurchaseHistoryRecord, error) {
	m.record("GetActivePurchaseHistory", ctx, asOf, accountIDs, externalIDsByProvider)
	args := m.Called(ctx, asOf, accountIDs, externalIDsByProvider)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseHistoryRecord)
	if !ok {
		m.reporter.wrongReturnType("GetActivePurchaseHistory", "[]config.PurchaseHistoryRecord", args.Get(0))
	}
	return v, args.Error(1)
}

// GetPurchaseHistoryFiltered mocks the GetPurchaseHistoryFiltered operation (issue #701).
func (m *MockConfigStore) GetPurchaseHistoryFiltered(ctx context.Context, filter config.PurchaseHistoryFilter) ([]config.PurchaseHistoryRecord, error) {
	m.record("GetPurchaseHistoryFiltered", ctx, filter)
	args := m.Called(ctx, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.PurchaseHistoryRecord)
	if !ok {
		m.reporter.wrongReturnType("GetPurchaseHistoryFiltered", "[]config.PurchaseHistoryRecord", args.Get(0))
	}
	return v, args.Error(1)
}

// GetPurchaseHistoryByPurchaseID mocks the single-row lookup by purchase_id (issues #290, #292).
func (m *MockConfigStore) GetPurchaseHistoryByPurchaseID(ctx context.Context, purchaseID string) (*config.PurchaseHistoryRecord, error) {
	m.record("GetPurchaseHistoryByPurchaseID", ctx, purchaseID)
	args := m.Called(ctx, purchaseID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.PurchaseHistoryRecord)
	if !ok {
		m.reporter.wrongReturnType("GetPurchaseHistoryByPurchaseID", "*config.PurchaseHistoryRecord", args.Get(0))
	}
	return v, args.Error(1)
}

// MarkPurchaseRevoked mocks the MarkPurchaseRevoked operation (issue #290).
func (m *MockConfigStore) MarkPurchaseRevoked(ctx context.Context, purchaseID string, revokedAt time.Time, revokedVia, supportCaseID string, calcRefundAmount *float64, calcRefundCurrency string) error {
	m.record("MarkPurchaseRevoked", ctx, purchaseID, revokedAt, revokedVia, supportCaseID, calcRefundAmount, calcRefundCurrency)
	args := m.Called(ctx, purchaseID, revokedAt, revokedVia, supportCaseID, calcRefundAmount, calcRefundCurrency)
	return args.Error(0)
}

// FlipPurchaseRevocationInFlight mocks FlipPurchaseRevocationInFlight (issue #290 Finding #6).
// Uses the isExpected default-or-dispatch pattern: tests that only verify
// MarkPurchaseRevoked do not need to set an expectation for this best-effort call.
func (m *MockConfigStore) FlipPurchaseRevocationInFlight(ctx context.Context, purchaseID string) error {
	m.record("FlipPurchaseRevocationInFlight", ctx, purchaseID)
	if !isExpected(&m.Mock, "FlipPurchaseRevocationInFlight") {
		return nil
	}
	args := m.Called(ctx, purchaseID)
	return args.Error(0)
}

// ClearRevocationInFlight mocks ClearRevocationInFlight (issue #290 second-wave CR Finding D).
// Uses the isExpected default-or-dispatch pattern: tests that only test error paths
// where Azure never actually returned do not need to register an expectation.
func (m *MockConfigStore) ClearRevocationInFlight(ctx context.Context, purchaseID string) error {
	m.record("ClearRevocationInFlight", ctx, purchaseID)
	if !isExpected(&m.Mock, "ClearRevocationInFlight") {
		return nil
	}
	args := m.Called(ctx, purchaseID)
	return args.Error(0)
}

// GetPurchaseHistoryInFlight mocks GetPurchaseHistoryInFlight (issue #290 Finding #6).
func (m *MockConfigStore) GetPurchaseHistoryInFlight(ctx context.Context) ([]*config.PurchaseHistoryRecord, error) {
	m.record("GetPurchaseHistoryInFlight", ctx)
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]*config.PurchaseHistoryRecord)
	if !ok {
		m.reporter.wrongReturnType("GetPurchaseHistoryInFlight", "[]*config.PurchaseHistoryRecord", args.Get(0))
	}
	return v, args.Error(1)
}

// MarkRevocationReturnPosted mocks MarkRevocationReturnPosted (issue #1523).
// Uses the isExpected default-or-dispatch pattern: revoke tests that only
// verify MarkPurchaseRevoked do not need an expectation for this stamp.
func (m *MockConfigStore) MarkRevocationReturnPosted(ctx context.Context, purchaseID string) error {
	m.record("MarkRevocationReturnPosted", ctx, purchaseID)
	if !isExpected(&m.Mock, "MarkRevocationReturnPosted") {
		return nil
	}
	args := m.Called(ctx, purchaseID)
	return args.Error(0)
}

// ReleaseStaleRevocationsInFlight mocks ReleaseStaleRevocationsInFlight
// (issue #1523). Default-or-dispatch: with no expectation it releases nothing.
func (m *MockConfigStore) ReleaseStaleRevocationsInFlight(ctx context.Context, after time.Duration) ([]string, error) {
	m.record("ReleaseStaleRevocationsInFlight", ctx, after)
	if !isExpected(&m.Mock, "ReleaseStaleRevocationsInFlight") {
		return nil, nil
	}
	args := m.Called(ctx, after)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]string)
	if !ok {
		m.reporter.wrongReturnType("ReleaseStaleRevocationsInFlight", "[]string", args.Get(0))
	}
	return v, args.Error(1)
}

// UpdatePurchaseHistoryListing mocks stamping listing_id and listing_state (issue #292).
func (m *MockConfigStore) UpdatePurchaseHistoryListing(ctx context.Context, purchaseID, listingID, listingState string) error {
	m.record("UpdatePurchaseHistoryListing", ctx, purchaseID, listingID, listingState)
	args := m.Called(ctx, purchaseID, listingID, listingState)
	return args.Error(0)
}

// GetMarketplaceListingAttempt mocks reading a row's listing attempt (issue #525).
func (m *MockConfigStore) GetMarketplaceListingAttempt(ctx context.Context, purchaseID string) (*config.MarketplaceListingAttempt, error) {
	m.record("GetMarketplaceListingAttempt", ctx, purchaseID)
	args := m.Called(ctx, purchaseID)
	var attempt *config.MarketplaceListingAttempt
	if v := args.Get(0); v != nil {
		a, ok := v.(*config.MarketplaceListingAttempt)
		if !ok {
			m.reporter.wrongReturnType("GetMarketplaceListingAttempt", "*config.MarketplaceListingAttempt", v)
		}
		attempt = a
	}
	return attempt, args.Error(1)
}

// ResolveMarketplaceListingAttempt mocks the fenced recovery write of the cancel endpoint (issue #525).
func (m *MockConfigStore) ResolveMarketplaceListingAttempt(ctx context.Context, purchaseID string, seen config.MarketplaceListingAttempt, listingID, state string) (bool, error) {
	m.record("ResolveMarketplaceListingAttempt", ctx, purchaseID, seen, listingID, state)
	args := m.Called(ctx, purchaseID, seen, listingID, state)
	return args.Bool(0), args.Error(1)
}

// StampOfferingClass mocks persisting the offering_class fetched from AWS (issue #292).
func (m *MockConfigStore) StampOfferingClass(ctx context.Context, purchaseID, offeringClass string) error {
	m.record("StampOfferingClass", ctx, purchaseID, offeringClass)
	args := m.Called(ctx, purchaseID, offeringClass)
	return args.Error(0)
}

// ClaimMarketplaceListingSlot mocks the atomic listing-slot claim (issue #292).
func (m *MockConfigStore) ClaimMarketplaceListingSlot(ctx context.Context, purchaseID, clientToken string, priceSchedule []byte) (*config.MarketplaceListingClaim, error) {
	m.record("ClaimMarketplaceListingSlot", ctx, purchaseID, clientToken, priceSchedule)
	args := m.Called(ctx, purchaseID, clientToken, priceSchedule)
	var claim *config.MarketplaceListingClaim
	if v := args.Get(0); v != nil {
		c, ok := v.(*config.MarketplaceListingClaim)
		if !ok {
			m.reporter.wrongReturnType("ClaimMarketplaceListingSlot", "*config.MarketplaceListingClaim", v)
		}
		claim = c
	}
	return claim, args.Error(1)
}

// ReleaseMarketplaceListingClaim mocks releasing a pending listing claim (issue #525).
func (m *MockConfigStore) ReleaseMarketplaceListingClaim(ctx context.Context, purchaseID, clientToken, priorState string, keepAttempt bool) error {
	m.record("ReleaseMarketplaceListingClaim", ctx, purchaseID, clientToken, priorState, keepAttempt)
	args := m.Called(ctx, purchaseID, clientToken, priorState, keepAttempt)
	return args.Error(0)
}

// ListMarketplaceListingsToPoll mocks the marketplace status poller's row scan (issue #27).
func (m *MockConfigStore) ListMarketplaceListingsToPoll(ctx context.Context) ([]config.PurchaseHistoryRecord, error) {
	m.record("ListMarketplaceListingsToPoll", ctx)
	args := m.Called(ctx)
	var rows []config.PurchaseHistoryRecord
	if v := args.Get(0); v != nil {
		r, ok := v.([]config.PurchaseHistoryRecord)
		if !ok {
			m.reporter.wrongReturnType("ListMarketplaceListingsToPoll", "[]config.PurchaseHistoryRecord", v)
		}
		rows = r
	}
	return rows, args.Error(1)
}

// RecordMarketplaceListingActive mocks stamping listed_at on an active listing (issue #27).
func (m *MockConfigStore) RecordMarketplaceListingActive(ctx context.Context, purchaseID, listingID string, listedAt time.Time) (bool, error) {
	m.record("RecordMarketplaceListingActive", ctx, purchaseID, listingID, listedAt)
	args := m.Called(ctx, purchaseID, listingID, listedAt)
	return args.Bool(0), args.Error(1)
}

// SettleMarketplaceListing mocks recording a closed or canceled listing (issue #27).
func (m *MockConfigStore) SettleMarketplaceListing(ctx context.Context, settlement config.MarketplaceListingSettlement) (bool, error) {
	m.record("SettleMarketplaceListing", ctx, settlement)
	args := m.Called(ctx, settlement)
	return args.Bool(0), args.Error(1)
}

// GetMarketplaceListingProceedsTotals mocks the per-RI marketplace proceeds totals (issue #1494).
func (m *MockConfigStore) GetMarketplaceListingProceedsTotals(ctx context.Context, purchaseIDs, accountIDs []string, externalIDsByProvider map[string][]string) ([]config.MarketplaceListingProceedsTotal, error) {
	m.record("GetMarketplaceListingProceedsTotals", ctx, purchaseIDs, accountIDs, externalIDsByProvider)
	args := m.Called(ctx, purchaseIDs, accountIDs, externalIDsByProvider)
	var totals []config.MarketplaceListingProceedsTotal
	if v := args.Get(0); v != nil {
		t, ok := v.([]config.MarketplaceListingProceedsTotal)
		if !ok {
			m.reporter.wrongReturnType("GetMarketplaceListingProceedsTotals", "[]config.MarketplaceListingProceedsTotal", v)
		}
		totals = t
	}
	return totals, args.Error(1)
}
