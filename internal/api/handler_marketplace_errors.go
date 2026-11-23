package api

import (
	"errors"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	smithy "github.com/aws/smithy-go"
)

// awsMarketplaceClientFaultCodes is the set of AWS error codes that represent
// client-side faults for Marketplace listing operations. These map to 4xx
// responses so the caller receives an actionable message. Server-side AWS
// errors remain 5xx. SellerNotRegistered is a client fault too, but
// mapAWSMarketplaceError answers it before consulting this set.
var awsMarketplaceClientFaultCodes = map[string]bool{
	"InvalidReservedInstancesId":            true,
	"InvalidReservedInstancesId.NotFound":   true,
	"InvalidParameterValue":                 true,
	"InvalidParameter":                      true,
	"IncorrectState":                        true,
	"InvalidReservedInstancesListingId":     true,
	"ReservedInstancesListingAlreadyExists": true,
}

// awsMarketplaceDeploymentFaultCodes are AWS codes that mean this deployment's
// own credentials or IAM role lack a permission. That is an operator defect,
// not caller input, so they map to 502 even though AWS tags them client faults.
var awsMarketplaceDeploymentFaultCodes = map[string]bool{
	"AuthFailure":           true,
	"UnauthorizedOperation": true,
}

const marketplaceRoleAssumptionMessage = "could not assume the role configured for this account"

// marketplaceSellerNotRegisteredMessage replaces AWS's SellerNotRegistered text
// (issue #27), which says only that the account is not a registered seller.
// Registration is a one-time step in the AWS console that this app cannot do:
// the account's root user registers it with a bank account that has a US
// address and completes a tax interview.
const marketplaceSellerNotRegisteredMessage = "this AWS account is not registered as a Reserved Instance Marketplace seller. " +
	"Its root user must register it as a seller, with a bank account that has a US address, before it can list Reserved Instances: " +
	"https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ri-market-general.html#ri-market-seller-profile"

// isRoleAssumptionFailure reports whether err came from the STS AssumeRole call
// that the credential provider makes lazily inside an EC2 request. Its text
// names the host account and role ARNs, so it must never reach the caller.
func isRoleAssumptionFailure(err error) bool {
	return roleAssumptionOperationError(err) != nil
}

// roleAssumptionOperationError returns the STS AssumeRole operation error in
// err's chain, or nil when there is none.
func roleAssumptionOperationError(err error) *smithy.OperationError {
	for ; err != nil; err = errors.Unwrap(err) {
		var opErr *smithy.OperationError
		if errors.As(err, &opErr) && opErr.ServiceID == "STS" && strings.HasPrefix(opErr.OperationName, "AssumeRole") {
			return opErr
		}
	}
	return nil
}

// marketplaceSellerNotRegisteredReason is the stable details.reason (flattened
// into the JSON body as "reason") that marks the SellerNotRegistered 400, so a
// client can branch on it instead of matching the message.
const marketplaceSellerNotRegisteredReason = "seller_not_registered"

// mapAWSMarketplaceError maps an AWS SDK error to an appropriate ClientError.
// AWS client-fault errors (4xx-category codes) produce a 400 with a category
// message, never the AWS text, which carries ARNs and request IDs. SellerNotRegistered
// is the exception: its 400 carries marketplaceSellerNotRegisteredMessage and
// reason seller_not_registered instead. All other errors, including AWS
// AuthFailure and UnauthorizedOperation (this deployment's IAM gap), produce a
// 502. STS AssumeRole failures are classified by
// roleAssumptionClientError and never echo the AWS text.
func mapAWSMarketplaceError(opMsg string, err error) error {
	if opErr := roleAssumptionOperationError(err); opErr != nil {
		return roleAssumptionClientError(opMsg, opErr)
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		if apiErr.ErrorCode() == "SellerNotRegistered" {
			return NewClientErrorWithDetails(400, marketplaceSellerNotRegisteredMessage,
				map[string]any{"reason": marketplaceSellerNotRegisteredReason})
		}
		if !awsMarketplaceDeploymentFaultCodes[apiErr.ErrorCode()] &&
			(awsMarketplaceClientFaultCodes[apiErr.ErrorCode()] || apiErr.ErrorFault() == smithy.FaultClient) {
			logging.Warnf("marketplace: %s: provider rejected the request: %v", opMsg, err)
			category, msg := purchase.SummarizeProviderError(err)
			if category == purchase.ErrCategoryUnknown {
				msg = marketplaceRejectedMessage
			}
			return NewClientError(400, msg)
		}
	}
	logging.Warnf("marketplace: %s: %v", opMsg, err)
	_, msg := purchase.SummarizeProviderError(err)
	return NewClientError(502, opMsg+": "+msg)
}

// marketplaceRejectedMessage is the fixed text for a client-fault rejection
// the error classifier has no category for; the provider's text stays in the log.
const marketplaceRejectedMessage = "AWS rejected the listing request as invalid: check the listing parameters"
