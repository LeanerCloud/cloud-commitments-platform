package purchase

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/stretchr/testify/assert"
)

var _ LadderFireStore = (*config.PostgresStore)(nil)

type stubScheduledNotifier struct{}

func (stubScheduledNotifier) SendPurchaseScheduledNotification(context.Context, email.NotificationData) error {
	return nil
}

func fireGlobal(mut func(*config.GlobalConfig)) *config.GlobalConfig {
	to := "ops@example.com"
	g := &config.GlobalConfig{LadderingEnabled: true, LadderExecutionEnabled: true, PurchaseDelayHours: 48, NotificationEmail: &to}
	if mut != nil {
		mut(g)
	}
	return g
}

func fireDeps() LadderFireDeps {
	return LadderFireDeps{Notifier: stubScheduledNotifier{}, OwnAccountID: "own-acct", DashboardURL: "https://dash.example.com"}
}

func TestLadderSwitchRefusal(t *testing.T) {
	cases := []struct {
		name string
		g    *config.GlobalConfig
		want string
	}{
		{"nil", nil, "global config unavailable"},
		{"laddering off", fireGlobal(func(g *config.GlobalConfig) { g.LadderingEnabled = false }), "laddering_enabled is off"},
		{"execution off", fireGlobal(func(g *config.GlobalConfig) { g.LadderExecutionEnabled = false }), "ladder_execution_enabled is off"},
		{"4-eyes on", fireGlobal(func(g *config.GlobalConfig) { g.RequireDifferentApprover = true }), "require_different_approver is on (ladder executions have no approver)"},
		{"all on", fireGlobal(nil), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { assert.Equal(t, c.want, ladderSwitchRefusal(c.g)) })
	}
}

func TestLadderNoticeRefusal(t *testing.T) {
	blank := "  "
	cases := []struct {
		name string
		g    *config.GlobalConfig
		mut  func(*LadderFireDeps)
		want string
	}{
		{"delay 23 refused", fireGlobal(func(g *config.GlobalConfig) { g.PurchaseDelayHours = 23 }), nil, "purchase_delay_hours is under 24, so there is no revoke window"},
		{"negative delay clamps to 0 and is refused", fireGlobal(func(g *config.GlobalConfig) { g.PurchaseDelayHours = -5 }), nil, "purchase_delay_hours is under 24, so there is no revoke window"},
		{"delay 24 allowed", fireGlobal(func(g *config.GlobalConfig) { g.PurchaseDelayHours = 24 }), nil, ""},
		{"delay 168 allowed", fireGlobal(func(g *config.GlobalConfig) { g.PurchaseDelayHours = 168 }), nil, ""},
		{"delay 200 clamps to 168 and is allowed", fireGlobal(func(g *config.GlobalConfig) { g.PurchaseDelayHours = 200 }), nil, ""},
		{"no notifier", fireGlobal(nil), func(d *LadderFireDeps) { d.Notifier = nil }, "no notifier is configured"},
		{"nil recipient", fireGlobal(func(g *config.GlobalConfig) { g.NotificationEmail = nil }), nil, "notification_email is empty"},
		{"blank recipient", fireGlobal(func(g *config.GlobalConfig) { g.NotificationEmail = &blank }), nil, "notification_email is empty"},
		{"no dashboard url", fireGlobal(nil), func(d *LadderFireDeps) { d.DashboardURL = " " }, "dashboard URL is empty, so the notice has no revoke link"},
		{"all fine", fireGlobal(nil), nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := fireDeps()
			if c.mut != nil {
				c.mut(&d)
			}
			assert.Equal(t, c.want, ladderNoticeRefusal(c.g, d))
		})
	}
}

func TestLadderGlobalRefusalOrder(t *testing.T) {
	// Both a switch and the notice would refuse: the switch is reported.
	g := fireGlobal(func(g *config.GlobalConfig) { g.LadderingEnabled = false; g.PurchaseDelayHours = 1 })
	assert.Equal(t, "laddering_enabled is off", ladderGlobalRefusal(g, fireDeps()))
	g = fireGlobal(func(g *config.GlobalConfig) { g.PurchaseDelayHours = 1 })
	assert.Equal(t, "purchase_delay_hours is under 24, so there is no revoke window", ladderGlobalRefusal(g, fireDeps()))
	assert.Empty(t, ladderGlobalRefusal(fireGlobal(nil), fireDeps()))
}

func TestLadderConfigRefusal(t *testing.T) {
	capv := 10.0
	good := func(mut func(*config.LadderConfigDB)) config.LadderConfigDB {
		c := config.LadderConfigDB{Enabled: true, Provider: "aws", Mode: "auto_approve", MaxHourlyCommitPerRun: &capv}
		if mut != nil {
			mut(&c)
		}
		return c
	}
	own := &config.CloudAccount{ExternalID: "own-acct"}
	cases := []struct {
		name string
		cfg  config.LadderConfigDB
		acct *config.CloudAccount
		want string
	}{
		{"disabled", good(func(c *config.LadderConfigDB) { c.Enabled = false }), own, "config is disabled"},
		{"azure", good(func(c *config.LadderConfigDB) { c.Provider = "azure" }), own, "provider azure is not aws"},
		{"email approval", good(func(c *config.LadderConfigDB) { c.Mode = "email_approval" }), own, "mode email_approval is not auto_approve"},
		{"no cap", good(func(c *config.LadderConfigDB) { c.MaxHourlyCommitPerRun = nil }), own, "max_hourly_commit_per_run is not set"},
		{"no account", good(nil), nil, "cloud account not found"},
		{"other account", good(nil), &config.CloudAccount{ExternalID: "other"}, "cloud account is not the account this process runs in"},
		{"empty own account never matches an empty external id", good(nil), &config.CloudAccount{ExternalID: ""}, "the account this process runs in is unknown"},
		{"ok", good(nil), own, ""},
		{"disabled wins over azure", good(func(c *config.LadderConfigDB) { c.Enabled = false; c.Provider = "azure" }), own, "config is disabled"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := fireDeps()
			if c.name == "empty own account never matches an empty external id" {
				d.OwnAccountID = ""
			}
			assert.Equal(t, c.want, ladderConfigRefusal(&c.cfg, c.acct, d))
		})
	}
}

func TestLadderSweepLayers(t *testing.T) {
	both := []ladder.LayerType{ladder.LayerComputeSP, ladder.LayerConvertibleRI}
	onlySP := []ladder.LayerType{ladder.LayerComputeSP}
	class := func(c string) *config.GlobalConfig { return &config.GlobalConfig{OfferingClass: c} }
	assert.Equal(t, onlySP, ladderSweepLayers(both, class("standard")))
	assert.Equal(t, both, ladderSweepLayers(both, class("")))
	assert.Equal(t, both, ladderSweepLayers(both, class("convertible")))
	assert.Equal(t, onlySP, ladderSweepLayers(onlySP, class("standard")))
}
