package credentials

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// ErrNotHostAccount means a role_arn account with no role ARN could not be
// confirmed as the AWS account CUDly's own credentials belong to.
var ErrNotHostAccount = errors.New("credentials: aws_role_arn is empty but the account is not confirmed as the CUDly host account")

// CallerIdentityClient is satisfied by *sts.Client.
type CallerIdentityClient interface {
	GetCallerIdentity(ctx context.Context, params *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// VerifyHostAccount confirms account.ExternalID is the AWS account that hostSTS,
// an STS client signed with the host's ambient credentials, runs as.
func VerifyHostAccount(ctx context.Context, account *config.CloudAccount, hostSTS CallerIdentityClient) error {
	if hostSTS == nil {
		return fmt.Errorf("%w: no STS client to resolve the host identity (account %s)", ErrNotHostAccount, account.ID)
	}
	out, err := hostSTS.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return fmt.Errorf("%w: resolve host identity for account %s: %w", ErrNotHostAccount, account.ID, err)
	}
	if out == nil || out.Account == nil || *out.Account == "" {
		return fmt.Errorf("%w: STS returned no host account ID (account %s)", ErrNotHostAccount, account.ID)
	}
	if account.ExternalID != *out.Account {
		return fmt.Errorf("%w: account %s has external_id %q, host is %s; set aws_role_arn", ErrNotHostAccount, account.ID, account.ExternalID, *out.Account)
	}
	return nil
}
