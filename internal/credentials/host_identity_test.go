package credentials

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type blockingCallerIdentitySTS struct{}

func (blockingCallerIdentitySTS) GetCallerIdentity(ctx context.Context, _ *sts.GetCallerIdentityInput, _ ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestVerifyHostAccount_ErrorDoesNotLeakHostIdentity(t *testing.T) {
	const hostID = "111111111111"
	const rawSTSError = "operation error STS: GetCallerIdentity, https response error StatusCode: 403"
	account := &config.CloudAccount{ID: "tenant-acct", ExternalID: "222222222222", AWSAuthMode: "role_arn"}

	for name, hostSTS := range map[string]CallerIdentityClient{
		"different host account": &callerIdentitySTS{account: hostID},
		"sts error":              &callerIdentitySTS{err: errors.New(rawSTSError + " arn:aws:iam::" + hostID + ":role/cudly")},
		"empty host account":     &callerIdentitySTS{},
		"no sts client":          nil,
	} {
		t.Run(name, func(t *testing.T) {
			err := VerifyHostAccount(context.Background(), account, hostSTS)
			require.ErrorIs(t, err, ErrNotHostAccount)
			assert.Equal(t, ErrNotHostAccount.Error(), err.Error())
			assert.NotContains(t, err.Error(), hostID)
			assert.NotContains(t, err.Error(), "StatusCode")
			assert.NotContains(t, err.Error(), "GetCallerIdentity")
		})
	}
}

func TestVerifyHostAccount_BoundsSTSCall(t *testing.T) {
	old := hostIdentityTimeout
	hostIdentityTimeout = 50 * time.Millisecond
	t.Cleanup(func() { hostIdentityTimeout = old })

	account := &config.CloudAccount{ID: "tenant-acct", ExternalID: "222222222222", AWSAuthMode: "role_arn"}
	done := make(chan error, 1)
	go func() { done <- VerifyHostAccount(context.Background(), account, blockingCallerIdentitySTS{}) }()

	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrNotHostAccount)
		assert.Equal(t, ErrNotHostAccount.Error(), err.Error())
	case <-time.After(5 * time.Second):
		t.Fatal("VerifyHostAccount did not return after the STS timeout")
	}
}
