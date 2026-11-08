package purchase

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveOpsHint(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty input", "", ""},
		{"transient SES throttle (no match)", "send failed: SES throttle exceeded", ""},
		{"FROM_EMAIL exact", "FROM_EMAIL not configured for this deployment", "Set FROM_EMAIL tfvar then retry"},
		{"SES sandbox case-insensitive", "send failed: ses sandbox active", "Move SES out of sandbox or verify recipient, then retry"},
		{"domain not verified", "send failed: SES domain not verified", "Verify SES domain in AWS console, then retry"},
		{"IAM denied", "AssumeRole error: IAM denied", "Grant the deploy role missing IAM permission, then retry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, ResolveOpsHint(tt.input))
		})
	}
}
