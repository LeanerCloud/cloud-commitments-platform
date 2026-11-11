package purchase

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// ladderExecutionRefusal is the execute-time recheck for ladder purchases
// (platform#118, PR4r). A ladder execution can sit approved or scheduled for
// hours, so the switches that allowed it to be created are read again here,
// right before any provider call: both global ladder flags, the ladder config
// (enabled, auto_approve) and the run (not canceled).
//
// An execution belongs to a ladder tranche when its idempotency key equals the
// tranche id. The lookup compares as text, so every other key shape
// ("<root>:<account>" multi-account keys, legacy execution ids, fingerprints)
// simply matches nothing and is not a ladder execution. Only a real lookup
// error refuses, because a recheck that cannot read the switches must not
// guess "allowed".
//
// A refusal is wrapped by the caller as a beforeSubmission error: the row ends
// failed with no_submission stamped, which lets the ladder reconciler fail the
// tranche and release its netting (decideFiredTranche reads that proof).
//
// Accepted residual race: the switch can be flipped after this check passes
// and before the provider call. That window is milliseconds and no lock is
// held across a provider call, so it cannot be closed here.
func (m *Manager) ladderExecutionRefusal(ctx context.Context, exec *config.PurchaseExecution) error {
	gate, err := m.config.GetLadderExecutionGate(ctx, exec.IdempotencyKey)
	if err != nil {
		return fmt.Errorf("refusing to execute: ladder tranche lookup failed: %w", err)
	}
	if gate == nil {
		return nil
	}
	global, err := m.ladderFlagsRefusal(ctx)
	if err != nil {
		return err
	}
	if err := ladderGateRefusal(gate); err != nil {
		return err
	}
	return ladderApprovalRefusal(exec, gate, global)
}

func (m *Manager) ladderFlagsRefusal(ctx context.Context) (*config.GlobalConfig, error) {
	global, err := m.config.GetGlobalConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("refusing to execute ladder purchase: global config unreadable: %w", err)
	}
	if global == nil {
		return nil, errors.New("refusing to execute ladder purchase: global config unavailable")
	}
	if !global.LadderingEnabled {
		return nil, errors.New("refusing to execute ladder purchase: ladder planning is switched off")
	}
	if !global.LadderExecutionEnabled {
		return nil, errors.New("refusing to execute ladder purchase: ladder execution is switched off")
	}
	return global, nil
}

// ladderApprovalRefusal covers what a ladder purchase must have beyond the
// switches. The scheduled-purchase notice must have gone out (the ladder fire
// sweep stamps notification_sent only after a notice was sent). 4-eyes
// mode is refused because a ladder purchase has no second approver. The
// Manager buys the global offering class, so an RI-layer tranche cannot run
// while that class is anything but convertible.
func ladderApprovalRefusal(exec *config.PurchaseExecution, gate *config.LadderExecutionGate, global *config.GlobalConfig) error {
	switch {
	case exec.NotificationSent == nil:
		return errors.New(ladderNoticeNotSentNote)
	case global.RequireDifferentApprover:
		return errors.New(ladderNeedsSecondApproverNote)
	case gate.LayerType == ladder.LayerConvertibleRI && global.OfferingClass != "" && global.OfferingClass != "convertible":
		return errors.New(ladderRINeedsConvertibleNote)
	}
	return nil
}

const (
	ladderNoticeNotSentNote       = "refusing to execute ladder purchase: its scheduled-purchase notice was not sent"
	ladderNeedsSecondApproverNote = "refusing to execute ladder purchase: purchases need a second approver"
	ladderRINeedsConvertibleNote  = "refusing to execute ladder purchase: RI ladder purchases need the convertible offering class"
)

func ladderGateRefusal(gate *config.LadderExecutionGate) error {
	switch {
	case gate.TrancheStatus == nil:
		return errors.New("refusing to execute ladder purchase: its tranche status is unknown")
	case config.LadderTrancheSettled(ladder.TrancheStatus(*gate.TrancheStatus)):
		return fmt.Errorf("refusing to execute ladder purchase: its tranche is %s", *gate.TrancheStatus)
	case gate.ConfigEnabled == nil || gate.ConfigMode == nil:
		return errors.New("refusing to execute ladder purchase: its ladder config no longer exists")
	case !*gate.ConfigEnabled:
		return errors.New("refusing to execute ladder purchase: its ladder config is disabled")
	case ladder.LadderMode(*gate.ConfigMode) != ladder.ModeAutoApprove:
		return fmt.Errorf("refusing to execute ladder purchase: ladder config mode is %q, not auto_approve", *gate.ConfigMode)
	case gate.RunStatus == nil:
		return errors.New("refusing to execute ladder purchase: its ladder run no longer exists")
	}
	return ladderRunRefusal(*gate.RunStatus)
}

// ladderRunRefusal parses the stored run status, so both stored spellings of
// canceled refuse (#1792), and a status the code does not know fails closed.
func ladderRunRefusal(status string) error {
	run, err := ladder.ParseRunStatus(status)
	if err != nil {
		return fmt.Errorf("refusing to execute ladder purchase: its ladder run status %q is unknown", status)
	}
	if run == ladder.RunStatusCanceled {
		return errors.New("refusing to execute ladder purchase: its ladder run was canceled")
	}
	return nil
}
