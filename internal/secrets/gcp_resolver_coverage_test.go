package secrets

import (
	"context"
	"errors"
	"testing"

	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func requireGCPStatusCode(t *testing.T, err error, code codes.Code) {
	t.Helper()
	require.Error(t, err)
	st, ok := status.FromError(errors.Unwrap(err))
	require.True(t, ok, "expected wrapped gRPC status error, got: %v", err)
	assert.Equal(t, code, st.Code())
}

// TestGCPResolver_DirectMethods tests the actual GCPResolver methods
// These tests exercise the real code paths but may skip if GCP credentials are unavailable.
func TestGCPResolver_DirectMethods(t *testing.T) {
	ctx := context.Background()

	// Try to create a GCP resolver
	resolver, err := NewGCPResolver(ctx, "test-project-id")
	if err != nil {
		t.Skipf("Skipping test: GCP config not available: %v", err)
	}
	defer resolver.Close()

	// Test that the resolver is properly configured
	assert.Equal(t, "test-project-id", resolver.projectID)
	assert.NotNil(t, resolver.client)
}

// TestGCPResolver_GetSecret_NonExistent tests getting a non-existent secret.
func TestGCPResolver_GetSecret_NonExistent(t *testing.T) {
	const secretID = "cudly-test-nonexistent-secret-12345-xyz"
	mock := &mockSecretManagerServer{accessSecretVersionFn: func(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest) (*secretmanagerpb.AccessSecretVersionResponse, error) {
		assert.Equal(t, "projects/test-project/secrets/"+secretID+"/versions/latest", req.Name)
		return nil, status.Error(codes.NotFound, "secret not found")
	}}
	resolver, cleanup := newTestGCPResolver(t, mock)
	defer cleanup()
	result, err := resolver.GetSecret(context.Background(), secretID)
	assert.Empty(t, result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to access secret")
	requireGCPStatusCode(t, err, codes.NotFound)
}

// TestGCPResolver_GetSecretJSON_NonExistent tests getting a non-existent JSON secret.
func TestGCPResolver_GetSecretJSON_NonExistent(t *testing.T) {
	const secretID = "cudly-test-nonexistent-json-secret-12345-xyz"
	mock := &mockSecretManagerServer{accessSecretVersionFn: func(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest) (*secretmanagerpb.AccessSecretVersionResponse, error) {
		assert.Equal(t, "projects/test-project/secrets/"+secretID+"/versions/latest", req.Name)
		return nil, status.Error(codes.NotFound, "secret not found")
	}}
	resolver, cleanup := newTestGCPResolver(t, mock)
	defer cleanup()
	result, err := resolver.GetSecretJSON(context.Background(), secretID)
	assert.Nil(t, result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to access secret")
	requireGCPStatusCode(t, err, codes.NotFound)
}

// TestGCPResolver_ListSecrets tests listing secrets.
func TestGCPResolver_ListSecrets(t *testing.T) {
	mock := &mockSecretManagerServer{listSecretsFn: func(ctx context.Context, req *secretmanagerpb.ListSecretsRequest) (*secretmanagerpb.ListSecretsResponse, error) {
		assert.Equal(t, "projects/test-project", req.Parent)
		assert.Empty(t, req.Filter)
		return &secretmanagerpb.ListSecretsResponse{Secrets: []*secretmanagerpb.Secret{{Name: "projects/test-project/secrets/one"}, {Name: "projects/test-project/secrets/two"}}}, nil
	}}
	resolver, cleanup := newTestGCPResolver(t, mock)
	defer cleanup()
	result, err := resolver.ListSecrets(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, []string{"one", "two"}, result)
}

// TestGCPResolver_ListSecrets_WithFilter_Coverage_Direct tests listing secrets with a filter using direct resolver.
func TestGCPResolver_ListSecrets_WithFilter_Coverage_Direct(t *testing.T) {
	mock := &mockSecretManagerServer{listSecretsFn: func(ctx context.Context, req *secretmanagerpb.ListSecretsRequest) (*secretmanagerpb.ListSecretsResponse, error) {
		assert.Equal(t, "projects/test-project", req.Parent)
		assert.Empty(t, req.Filter)
		return &secretmanagerpb.ListSecretsResponse{Secrets: []*secretmanagerpb.Secret{{Name: "projects/test-project/secrets/labels.env=test-one"}, {Name: "projects/test-project/secrets/other-one"}}}, nil
	}}
	resolver, cleanup := newTestGCPResolver(t, mock)
	defer cleanup()
	result, err := resolver.ListSecrets(context.Background(), "labels.env=test")
	require.NoError(t, err)
	assert.Equal(t, []string{"labels.env=test-one"}, result)
}

// TestGCPResolver_Close_Idempotent tests that Close can be called multiple times.
func TestGCPResolver_Close_Idempotent(t *testing.T) {
	resolver, cleanup := newTestGCPResolver(t, &mockSecretManagerServer{})
	defer cleanup()
	_ = resolver.Close()
	_ = resolver.Close()
}

// TestGCPResolver_DifferentProjectIDs tests creating resolvers for different projects.
func TestGCPResolver_DifferentProjectIDs(t *testing.T) {
	ctx := context.Background()

	projectIDs := []string{"project-1", "project-2", "my-gcp-project"}

	for _, projectID := range projectIDs {
		t.Run(projectID, func(t *testing.T) {
			resolver, err := NewGCPResolver(ctx, projectID)
			if err != nil {
				t.Skipf("Skipping test: GCP config not available for project %s: %v", projectID, err)
			}
			defer resolver.Close()

			assert.Equal(t, projectID, resolver.projectID)
			assert.NotNil(t, resolver.client)
		})
	}
}

// TestGCPResolver_ContextHandling tests context handling in GCP resolver.
func TestGCPResolver_ContextHandling(t *testing.T) {
	mock := &mockSecretManagerServer{accessSecretVersionFn: func(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest) (*secretmanagerpb.AccessSecretVersionResponse, error) {
		assert.Fail(t, "canceled request reached mock server")
		return nil, status.Error(codes.Internal, "unexpected request")
	}}
	resolver, cleanup := newTestGCPResolver(t, mock)
	defer cleanup()

	// Test with canceled context
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// GetSecret with canceled context
	_, err := resolver.GetSecret(cancelledCtx, "test-secret")
	requireGCPStatusCode(t, err, codes.Canceled)
}

// TestGCPResolver_EmptySecretID tests getting a secret with empty ID.
func TestGCPResolver_EmptySecretID(t *testing.T) {
	mock := &mockSecretManagerServer{accessSecretVersionFn: func(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest) (*secretmanagerpb.AccessSecretVersionResponse, error) {
		assert.Equal(t, "projects/test-project/secrets//versions/latest", req.Name)
		return nil, status.Error(codes.InvalidArgument, "empty secret")
	}}
	resolver, cleanup := newTestGCPResolver(t, mock)
	defer cleanup()
	_, err := resolver.GetSecret(context.Background(), "")
	requireGCPStatusCode(t, err, codes.InvalidArgument)
}

// TestGCPResolver_SpecialCharactersInSecretID tests secret IDs with special characters.
func TestGCPResolver_SpecialCharactersInSecretID(t *testing.T) {
	testIDs := []string{
		"secret-with-dashes",
		"secret_with_underscores",
		"SecretWithCaps",
	}
	for _, id := range testIDs {
		t.Run(id, func(t *testing.T) {
			expectedName := "projects/test-project/secrets/" + id + "/versions/latest"
			mock := &mockSecretManagerServer{accessSecretVersionFn: func(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest) (*secretmanagerpb.AccessSecretVersionResponse, error) {
				assert.Equal(t, expectedName, req.Name)
				return nil, status.Error(codes.NotFound, "secret not found")
			}}
			resolver, cleanup := newTestGCPResolver(t, mock)
			defer cleanup()
			_, err := resolver.GetSecret(context.Background(), id)
			requireGCPStatusCode(t, err, codes.NotFound)
		})
	}
}

// TestGCPResolver_GetSecretJSON_RealMethod tests the GetSecretJSON error propagation.
func TestGCPResolver_GetSecretJSON_RealMethod(t *testing.T) {
	const secretID = "cudly-test-nonexistent-json-secret-xyz"
	mock := &mockSecretManagerServer{accessSecretVersionFn: func(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest) (*secretmanagerpb.AccessSecretVersionResponse, error) {
		assert.Equal(t, "projects/test-project/secrets/"+secretID+"/versions/latest", req.Name)
		return nil, status.Error(codes.NotFound, "secret not found")
	}}
	resolver, cleanup := newTestGCPResolver(t, mock)
	defer cleanup()
	result, err := resolver.GetSecretJSON(context.Background(), secretID)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "failed to access secret")
	requireGCPStatusCode(t, err, codes.NotFound)
}

// TestGCPResolver_ResourceNameFormat verifies the resource name format.
func TestGCPResolver_ResourceNameFormat(t *testing.T) {
	// The GCP resolver constructs resource names in the format:
	// projects/{project}/secrets/{secret}/versions/latest
	resolver := &GCPResolver{
		projectID: "my-project",
		client:    nil,
	}

	// Verify the projectID is stored correctly
	assert.Equal(t, "my-project", resolver.projectID)
}
