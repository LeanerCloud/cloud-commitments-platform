package purchase

import (
	"context"
	"errors"
	"fmt"

	ec2svc "github.com/LeanerCloud/cloud-commitments-go/providers/aws/services/ec2"
	"github.com/aws/aws-sdk-go-v2/aws"
)

// LadderRIPricer is the production LadderRIPricerFactory: an EC2 client
// authenticated for the cloud account with the same per-account credential
// resolution the purchase itself uses. The only call it makes through the
// pricer is DescribeReservedInstancesOfferings (read-only); nothing is bought.
// Any failure returns an error and no pricer, so the sweep leaves the RI
// tranches unresolved.
func (m *Manager) LadderRIPricer(ctx context.Context, cloudAccountID, region string) (RIOfferingPricer, error) {
	if region == "" {
		return nil, errors.New("ladder RI pricer: region is required")
	}
	creds, err := m.ladderAWSCredentials(ctx, cloudAccountID)
	if err != nil {
		return nil, err
	}
	return newLadderRIPricer(creds, region), nil
}

// newLadderRIPricer builds the EC2 offering client for one region. The go
// library's client satisfies RIOfferingPricer.
func newLadderRIPricer(creds aws.CredentialsProvider, region string) RIOfferingPricer {
	return ec2svc.NewClient(aws.Config{Region: region, Credentials: creds})
}

// ladderAWSCredentials resolves the credentials of one AWS cloud account.
func (m *Manager) ladderAWSCredentials(ctx context.Context, cloudAccountID string) (aws.CredentialsProvider, error) {
	account, err := m.config.GetCloudAccount(ctx, cloudAccountID)
	if err != nil {
		return nil, fmt.Errorf("load cloud account %s: %w", cloudAccountID, err)
	}
	if account == nil {
		return nil, fmt.Errorf("cloud account %s not found", cloudAccountID)
	}
	provCfg, err := m.resolveAccountProvider(ctx, *account)
	if err != nil {
		return nil, err
	}
	if provCfg.AWSCredentialsProvider == nil {
		return nil, fmt.Errorf("cloud account %s resolved without AWS credentials", cloudAccountID)
	}
	return provCfg.AWSCredentialsProvider, nil
}
