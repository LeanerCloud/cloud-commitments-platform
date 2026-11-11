package server

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
)

// ladderNotifier returns the sender as a scheduled-notice notifier, or nil
// when there is no real one: a nil sender, the no-op sender (EMAIL_ENABLED
// false) or a typed-nil pointer all mean nobody can be told, and the sweep
// refuses to claim without a notifier.
func ladderNotifier(s email.SenderInterface) purchase.ScheduledNotifier {
	switch s.(type) {
	case nil, *email.NopSender:
		return nil
	}
	if rv := reflect.ValueOf(s); rv.Kind() == reflect.Ptr && rv.IsNil() {
		return nil
	}
	return s
}

// handleLadderFire claims due ladder tranches into scheduled executions. It is
// deliberately not registered as a scheduled task yet: nothing can run it
// until the ladder_fire task is wired in, and every purchase it makes ready
// still needs the revoke-window notice and the execute-time recheck.
func (app *Application) handleLadderFire(ctx context.Context) (purchase.LadderFireResult, error) {
	store, ok := app.Config.(purchase.LadderFireStore)
	if !ok {
		return purchase.LadderFireResult{}, fmt.Errorf("ladder_fire: config store %T cannot fire ladder tranches", app.Config)
	}
	accountID, region, err := app.resolveLadderAccount(ctx)
	if err != nil {
		return purchase.LadderFireResult{}, fmt.Errorf("ladder_fire: could not resolve caller AWS account for the single-account gate: %w", err)
	}
	res, err := purchase.RunLadderFire(ctx, purchase.LadderFireDeps{
		Store:        store,
		Registry:     purchase.NewLadderInstrumentRegistry(),
		Notifier:     ladderNotifier(app.Email),
		OwnAccountID: accountID,
		Region:       region,
		DashboardURL: strings.TrimRight(app.appConfig.DashboardURL, "/"),
		Now:          time.Now,
	})
	logging.Infof("ladder_fire: configs=%d skipped=%d due=%d unresolved=%d claimed=%d trimmed=%d errored=%d announced=%d notice_failed=%d notice_deferred=%d stamp_failed=%d",
		res.Configs, res.Skipped, res.Due, res.Unresolved, res.Claimed, res.Trimmed, res.Errored,
		res.Announced, res.NoticeFailed, res.NoticeDeferred, res.StampFailed)
	return res, err
}
