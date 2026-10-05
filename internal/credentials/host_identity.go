package credentials

import (
	"context"
	"errors"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// hostIdentityTimeout bounds the STS GetCallerIdentity call. A variable so
// tests can shorten it.
var hostIdentityTimeout = 5 * time.Second

// ErrNotHostAccount means a role_arn account with no role ARN could not be
// confirmed as the AWS account CUDly's own credentials belong to. Its text is
// stored in tenant-visible rows, so it never carries the host account id or
// raw STS errors; those are logged.
var ErrNotHostAccount = errors.New("credentials: aws_role_arn is empty but the account is not confirmed as the CUDly host account; set aws_role_arn")

// CallerIdentityClient is satisfied by *sts.Client.
type CallerIdentityClient interface {
	GetCallerIdentity(ctx context.Context, params *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// VerifyHostAccount confirms account.ExternalID is the AWS account that hostSTS,
// an STS client signed with the host's ambient credentials, runs as. Every
// failure returns ErrNotHostAccount unwrapped; the detail goes to the log only.
func VerifyHostAccount(ctx context.Context, account *config.CloudAccount, hostSTS CallerIdentityClient) error {
	if hostSTS == nil {
		logging.Warnf("host identity check for account %s: no STS client to resolve the host identity", account.ID)
		return ErrNotHostAccount
	}
	ctx, cancel := context.WithTimeout(ctx, hostIdentityTimeout)
	defer cancel()
	out, err := hostSTS.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		logging.Warnf("host identity check for account %s: resolve host identity: %v", account.ID, err)
		return ErrNotHostAccount
	}
	if out == nil || out.Account == nil || *out.Account == "" {
		logging.Warnf("host identity check for account %s: STS returned no host account ID", account.ID)
		return ErrNotHostAccount
	}
	if account.ExternalID != *out.Account {
		logging.Warnf("host identity check for account %s: external_id %q does not match host account %s", account.ID, account.ExternalID, *out.Account)
		return ErrNotHostAccount
	}
	return nil
}
