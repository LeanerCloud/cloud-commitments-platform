package archera

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// blockingResolver parks GetSecret until released, signaling when entered.
type blockingResolver struct {
	flakyResolver
	entered chan struct{}
	release chan struct{}
}

func (b *blockingResolver) GetSecret(context.Context, string) (string, error) {
	b.entered <- struct{}{}
	<-b.release
	return testKey, nil
}

func TestClient_WaiterHonoursItsOwnContextWhileAnotherBuilds(t *testing.T) {
	r := &blockingResolver{entered: make(chan struct{}, 1), release: make(chan struct{})}
	p := NewProvider(fullSettings(), r, nil)

	type result struct {
		c   any
		err error
	}
	first := make(chan result, 1)
	go func() {
		c, err := p.Client(context.Background())
		first <- result{c, err}
	}()
	<-r.entered

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() { _, err := p.Client(ctx); second <- err }()
	select {
	case err := <-second:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(200 * time.Millisecond):
		close(r.release)
		t.Fatal("waiting caller ignored its own ctx while another caller built the client")
	}

	close(r.release)
	select {
	case res := <-first:
		require.NoError(t, res.err)
		require.NotNil(t, res.c)
	case <-time.After(2 * time.Second):
		t.Fatal("first caller did not finish")
	}
	require.NotNil(t, p.client.Load(), "client cached after the first caller succeeds")
}

func TestClient_CachedClientSkipsHeldSemaphore(t *testing.T) {
	p := NewProvider(fullSettings(), envResolver(t), nil)
	c, err := p.Client(context.Background())
	require.NoError(t, err)

	p.sem <- struct{}{} // a build is "in flight"
	defer func() { <-p.sem }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := p.Client(ctx)
	require.NoError(t, err)
	require.Equal(t, c, got)
}
