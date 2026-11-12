//go:build integration

package api

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/require"
)

func TestR570Replay(t *testing.T) {
	ctx := context.Background()
	pg, err := testhelpers.SetupPostgresContainer(ctx, t)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pg.Cleanup(ctx) })
	require.NoError(t, migrations.RunMigrations(ctx, pg.DB.Pool(), "../database/postgres/migrations", "", ""))
	store := config.NewPostgresStore(pg.DB)
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateSession", ctx, "admin-token").Return(&Session{UserID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Email: "admin@example.com"}, nil)
	mockAuth.grantAdmin()
	handler := &Handler{config: store, auth: mockAuth}
	hdr := map[string]string{"Authorization": "Bearer admin-token"}

	raw, err := os.ReadFile(os.Getenv("R570_MATRIX"))
	require.NoError(t, err)
	var m map[string][]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &m))
	for name, bodies := range m {
		if len(bodies) == 0 {
			t.Logf("RESULT %-26s NO PUT", name)
			continue
		}
		g, err := store.GetGlobalConfig(ctx)
		require.NoError(t, err)
		// state after ANOTHER admin changed everything since this tab loaded
		em := "other@admin.io"
		g.EnabledProviders = []string{"aws", "azure"}
		g.NotificationEmail = &em
		g.AutoCollect, g.CollectionSchedule = true, "weekly"
		g.DefaultTerm, g.DefaultPayment, g.DefaultCoverage = 1, "no-upfront", 70
		g.NotificationDaysBefore = 4
		g.GracePeriodDays = map[string]int{"aws": 2, "azure": 2, "gcp": 2}
		g.RecommendationsCacheStaleHours, g.RecommendationsLookbackDays = 12, 30
		g.OfferingClass, g.RequireDifferentApprover, g.LadderingEnabled = "standard", true, true
		require.NoError(t, store.SaveGlobalConfig(ctx, g))
		_, err = handler.updateConfig(ctx, &events.LambdaFunctionURLRequest{Headers: hdr, Body: string(bodies[0])})
		g2, _ := store.GetGlobalConfig(ctx)
		e2 := ""
		if g2.NotificationEmail != nil {
			e2 = *g2.NotificationEmail
		}
		t.Logf("RESULT %-26s err=%v term=%d pay=%s cov=%v email=%s prov=%v sched=%s days=%d grace=%v stale=%d lb=%d oc=%s appr=%v ladder=%v body=%s",
			name, err, g2.DefaultTerm, g2.DefaultPayment, g2.DefaultCoverage, e2, g2.EnabledProviders, g2.CollectionSchedule, g2.NotificationDaysBefore, g2.GracePeriodDays, g2.RecommendationsCacheStaleHours, g2.RecommendationsLookbackDays, g2.OfferingClass, g2.RequireDifferentApprover, g2.LadderingEnabled, bodies[0])
	}
}
