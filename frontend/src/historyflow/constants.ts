

// Copy for a revoke whose Azure Return is not proven (#1522, #1584); keep it equal to
// revokePendingConfirmationMessage in internal/api/handler_purchases_revoke.go.
export const REVOCATION_PENDING_CONFIRMATION_COPY =
  'We could not confirm whether the refund for this purchase went through. Check the reservation in the Azure portal before trying again: if it still shows as active after about 90 minutes, you can revoke again.';

// AWS Marketplace fee/pricing constants (must stay in sync with handler_marketplace.go).
// awsMarketplaceFeePercent: transaction fee deducted from listing proceeds (published by AWS).
export const AWS_MARKETPLACE_FEE_PERCENT = 12;

// awsMarketplaceNetFactor: fraction of list price the seller receives after the fee.
export const AWS_MARKETPLACE_NET_FACTOR = 1 - AWS_MARKETPLACE_FEE_PERCENT / 100;

// Discount off residual RI value for the default list price; 1 - 5/100 mirrors
// awsMarketplaceBuyerDiscountFactor (0.95) in internal/api/handler_marketplace.go.
export const AWS_MARKETPLACE_BUYER_DISCOUNT_PERCENT = 5;

export const AWS_MARKETPLACE_BUYER_DISCOUNT = 1 - AWS_MARKETPLACE_BUYER_DISCOUNT_PERCENT / 100;
