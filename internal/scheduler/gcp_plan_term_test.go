package scheduler

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// collectGCPAmbientWith runs the ambient GCP collection with the given global
// config against recClient so callers assert which fetch method (and params) it saw. Platform issue #858.
func collectGCPAmbientWith(t *testing.T, globalCfg *config.GlobalConfig, recClient *MockRecommendationsClient) {
	t.Helper()
	t.Setenv("GCP_PROJECT_ID", "host-project")

	prov := new(MockProvider)
	prov.On("GetRecommendationsClient", mock.Anything).Return(recClient, nil)
	factory := new(MockProviderFactory)
	factory.On("CreateAndValidateProvider", mock.Anything, "gcp", mock.Anything).Return(prov, nil)
	store := new(MockConfigStore)
	store.On("ListCloudAccounts", mock.Anything, mock.Anything).Return([]config.CloudAccount{}, nil)

	s := &Scheduler{config: store, providerFactory: factory}
	_, _, _, err := s.collectGCPRecommendations(context.Background(), globalCfg)
	require.NoError(t, err)
}

func TestCollectGCPRecommendations_PlanTermOffKeepsGetAll(t *testing.T) {
	for name, cfg := range map[string]*config.GlobalConfig{
		"default":    {},
		"nil config": nil,
	} {
		t.Run(name, func(t *testing.T) {
			rc := new(MockRecommendationsClient)
			rc.On("GetAllRecommendations", mock.Anything).Return([]common.Recommendation{}, nil).Once()
			collectGCPAmbientWith(t, cfg, rc)
			rc.AssertExpectations(t)
			rc.AssertNotCalled(t, "GetRecommendations", mock.Anything, mock.Anything)
		})
	}
}

func TestCollectGCPRecommendations_PlanTermOnSetsParamFlag(t *testing.T) {
	rc := new(MockRecommendationsClient)
	rc.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(p *common.RecommendationParams) bool {
		return p.ExperimentalGCPPlanTerm && p.Service == ""
	})).Return([]common.Recommendation{}, nil).Once()
	collectGCPAmbientWith(t, &config.GlobalConfig{ExperimentalGCPPlanTerm: true}, rc)
	rc.AssertExpectations(t)
	assert.True(t, rc.AssertNotCalled(t, "GetAllRecommendations", mock.Anything))
}

// The registered-account path is the main production path: it must carry the
// flag too, not only the ambient fallback.
func TestCollectGCPRecommendations_PlanTermReachesRegisteredAccountPath(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg    *config.GlobalConfig
		expect func(*MockRecommendationsClient)
		on     bool
	}{
		"on": {&config.GlobalConfig{ExperimentalGCPPlanTerm: true}, func(c *MockRecommendationsClient) {
			c.On("GetRecommendations", mock.Anything, mock.MatchedBy(func(p *common.RecommendationParams) bool {
				return p.ExperimentalGCPPlanTerm
			})).Return([]common.Recommendation{}, nil).Once()
		}, true},
		"off": {&config.GlobalConfig{}, func(c *MockRecommendationsClient) {
			c.On("GetAllRecommendations", mock.Anything).Return([]common.Recommendation{}, nil).Once()
		}, false},
	} {
		t.Run(name, func(t *testing.T) {
			rc := new(MockRecommendationsClient)
			tc.expect(rc)
			prov := new(MockProvider)
			prov.On("GetRecommendationsClient", mock.Anything).Return(rc, nil)
			factory := new(MockProviderFactory)
			factory.On("CreateAndValidateProvider", mock.Anything, "gcp", mock.Anything).Return(prov, nil)
			store := new(MockConfigStore)
			store.On("ListCloudAccounts", mock.Anything, mock.Anything).Return([]config.CloudAccount{
				{ID: "acct-1", Provider: "gcp", ExternalID: "p1", GCPProjectID: "p1", GCPAuthMode: "application_default", Enabled: true},
			}, nil)

			s := &Scheduler{config: store, providerFactory: factory}
			_, _, _, err := s.collectGCPRecommendations(context.Background(), tc.cfg)
			require.NoError(t, err)
			rc.AssertExpectations(t)
			if tc.on {
				rc.AssertNotCalled(t, "GetAllRecommendations", mock.Anything)
			} else {
				rc.AssertNotCalled(t, "GetRecommendations", mock.Anything, mock.Anything)
			}
		})
	}
}
