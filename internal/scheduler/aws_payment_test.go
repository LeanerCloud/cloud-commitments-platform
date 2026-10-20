package scheduler

import (
	"context"
	"sync"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// The AWS per-service fallback must never hand a non-AWS payment option
// (Azure/GCP "monthly"/"upfront") to the AWS recommendations client (#1229).
func TestFetchAndConvert_AWSFallbackPaymentOption(t *testing.T) {
	cases := []struct{ stored, want string }{
		{"monthly", ""},
		{"upfront", ""},
		{"", ""},
		{"all-upfront", "all-upfront"},
		{"partial-upfront", "partial-upfront"},
		{"no-upfront", "no-upfront"},
	}
	for _, tc := range cases {
		t.Run("stored_"+tc.stored, func(t *testing.T) {
			var mu sync.Mutex
			var got []string
			client := new(MockRecommendationsClient)
			client.On("GetAllRecommendations", mock.Anything).Return([]common.Recommendation{}, nil)
			client.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(p *common.RecommendationParams) bool {
				mu.Lock()
				defer mu.Unlock()
				got = append(got, p.PaymentOption)
				return true
			})).Return([]common.Recommendation{}, nil)

			s := &Scheduler{}
			_, _, err := s.fetchAndConvert(context.Background(), newRecProvider(client), "aws", nil, "",
				&config.GlobalConfig{DefaultTerm: 1, DefaultPayment: tc.stored, RecommendationsLookbackDays: 30})
			require.NoError(t, err)
			require.NotEmpty(t, got)
			for _, g := range got {
				assert.Equal(t, tc.want, g)
			}
		})
	}
}
