package ec2offered

import (
	"context"
	"errors"
	"fmt"
	"testing"

	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	awsec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAPI struct {
	pages [][]awsec2types.InstanceType
	err   error
	calls int
}

func (f *fakeAPI) DescribeInstanceTypeOfferings(_ context.Context, _ *awsec2.DescribeInstanceTypeOfferingsInput, _ ...func(*awsec2.Options)) (*awsec2.DescribeInstanceTypeOfferingsOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := &awsec2.DescribeInstanceTypeOfferingsOutput{}
	for _, t := range f.pages[f.calls] {
		out.InstanceTypeOfferings = append(out.InstanceTypeOfferings, awsec2types.InstanceTypeOffering{InstanceType: t})
	}
	f.calls++
	if f.calls < len(f.pages) {
		tok := fmt.Sprint(f.calls)
		out.NextToken = &tok
	}
	return out, nil
}

func TestGetValidResourceTypes_PaginatesDedupesAndSorts(t *testing.T) {
	c := &Client{api: &fakeAPI{pages: [][]awsec2types.InstanceType{{"m5.large", "c5.large"}, {"c5.large", "a1.large"}}}}
	got, err := c.GetValidResourceTypes(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"a1.large", "c5.large", "m5.large"}, got)
}

func TestGetValidResourceTypes_WrapsError(t *testing.T) {
	boom := errors.New("boom")
	c := &Client{api: &fakeAPI{err: boom}}
	_, err := c.GetValidResourceTypes(context.Background())
	require.ErrorIs(t, err, boom)
}

func TestGetValidResourceTypes_ThreePages(t *testing.T) {
	c := &Client{api: &fakeAPI{pages: [][]awsec2types.InstanceType{{"a"}, {"b"}, {"c"}}}}
	got, err := c.GetValidResourceTypes(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, got)
}

func TestGetValidResourceTypes_PageCapFailsLoud(t *testing.T) {
	pages := make([][]awsec2types.InstanceType, maxPages+1)
	c := &Client{api: &fakeAPI{pages: pages}}
	_, err := c.GetValidResourceTypes(context.Background())
	require.ErrorContains(t, err, "pagination cap")
}
