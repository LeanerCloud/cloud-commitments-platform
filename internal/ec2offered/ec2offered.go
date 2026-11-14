// Package ec2offered restores GetValidResourceTypes on the provider EC2 client.
// The provider module dropped the method as unused on its side (go repo
// 7f024a6) while the RI exchange reshape paths still need the region's
// offered instance types.
package ec2offered

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	awsec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	awsprovider "github.com/LeanerCloud/cloud-commitments-go/providers/aws"
	ec2svc "github.com/LeanerCloud/cloud-commitments-go/providers/aws/services/ec2"
)

// Client is the provider EC2 client plus the offered-instance-type listing.
type Client struct {
	*ec2svc.Client
	api describeOfferingsAPI
}

type describeOfferingsAPI interface {
	DescribeInstanceTypeOfferings(ctx context.Context, params *awsec2.DescribeInstanceTypeOfferingsInput, optFns ...func(*awsec2.Options)) (*awsec2.DescribeInstanceTypeOfferingsOutput, error)
}

// New builds the client for the region in cfg.
func New(cfg aws.Config) *Client {
	return &Client{Client: awsprovider.NewEC2ClientDirect(cfg), api: awsec2.NewFromConfig(cfg)}
}

const (
	maxPages    = 20
	pageTimeout = 15 * time.Second
)

// GetValidResourceTypes lists the instance types offered in the region, sorted.
// It fails loudly past maxPages instead of returning a truncated catalog.
func (c *Client) GetValidResourceTypes(ctx context.Context) ([]string, error) {
	seen := map[string]struct{}{}
	var token *string
	for page := 0; ; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("pagination cap reached after %d pages for EC2 instance type offerings", maxPages)
		}
		pageCtx, cancel := context.WithTimeout(ctx, pageTimeout)
		out, err := c.api.DescribeInstanceTypeOfferings(pageCtx, &awsec2.DescribeInstanceTypeOfferingsInput{
			LocationType: awsec2types.LocationTypeRegion,
			MaxResults:   aws.Int32(1000),
			NextToken:    token,
		})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("failed to describe EC2 instance type offerings: %w", err)
		}
		for _, o := range out.InstanceTypeOfferings {
			seen[string(o.InstanceType)] = struct{}{}
		}
		if aws.ToString(out.NextToken) == "" {
			break
		}
		token = out.NextToken
	}
	types := make([]string, 0, len(seen))
	for t := range seen {
		types = append(types, t)
	}
	sort.Strings(types)
	return types, nil
}
