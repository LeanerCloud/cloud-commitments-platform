package api

import (
	"context"
	"net/mail"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
)

// notifyRegistrant sends an email about an approval or rejection.
// Errors are logged but not propagated (matching sendPurchaseApprovalEmail pattern).
func (h *Handler) notifyRegistrant(reg *config.AccountRegistration, data email.RegistrationDecisionData) {
	if h.emailNotifier == nil || reg.ContactEmail == "" {
		return
	}
	if err := h.emailNotifier.SendRegistrationDecisionNotification(context.Background(), reg.ContactEmail, data); err != nil {
		logging.Warnf("failed to send registration decision notification: %v", err)
	}
}

// registrationContactEmail returns the bare address in a registrant's
// contact_email, the form that is stored and later handed to the notifier,
// or a 400. It applies validateEmailFormat, the check the rest of the
// package uses (issue #311), so a value with a line break or a second
// address after it, or a host with no TLD, is refused rather than stored.
// A display-name form ("Jane <jane@example.com>") is accepted and reduced
// to its address. The check runs on that bare address, so one that would
// not parse again on its own (a quoted local part such as "a b"@example.com
// unquotes to a b@example.com) is refused too.
func registrationContactEmail(raw string) (string, error) {
	invalid := NewClientError(400, "contact_email must be a valid email address")
	addr, err := mail.ParseAddress(raw)
	if err != nil {
		return "", invalid
	}
	if validateEmailFormat(addr.Address) != nil {
		return "", invalid
	}
	return addr.Address, nil
}

// resolveRegistrationRecipients returns the To / Cc / admin-approver sets
// for a new-registration notification email.
//
// Rules:
//   - Every member of the Administrators group is an authorized reviewer.
//   - The first admin email is the To; remaining admins + the global
//     Settings → General notification email go on Cc.
//   - When no admin users are configured, falls through to the legacy
//     broadcast path by returning ("", nil, nil) — the SES sender
//     interprets this as "use SNS / static notify email".
//
// The account's own ContactEmail is NOT included in the approver set
// because the submitter can't review their own registration.
func (h *Handler) resolveRegistrationRecipients(ctx context.Context) (to string, cc, approvers []string) {
	adminEmails := h.gatherAdminEmails(ctx)
	globalNotify := h.globalNotificationEmail(ctx)

	if len(adminEmails) == 0 {
		// Preserve the historical SNS-broadcast path for deployments
		// that never provisioned admin users with emails.
		return "", nil, nil
	}

	to = adminEmails[0]
	approvers = append([]string(nil), adminEmails...)

	seen := map[string]bool{strings.ToLower(to): true}
	appendIfNew := func(addr string) {
		norm := strings.ToLower(strings.TrimSpace(addr))
		if norm == "" || seen[norm] {
			return
		}
		seen[norm] = true
		cc = append(cc, addr)
	}
	for _, addr := range adminEmails[1:] {
		appendIfNew(addr)
	}
	appendIfNew(globalNotify)
	return to, cc, approvers
}

// gatherAdminEmails returns the deduped, insertion-ordered list of emails
// for every authorized reviewer, i.e. every member of the Administrators group
// (the group-membership replacement for the former role == "admin" check;
// issue #907). Transport errors are logged and result in an empty return so
// registration notifications don't block on auth-store hiccups.
func (h *Handler) gatherAdminEmails(ctx context.Context) []string {
	if h.auth == nil {
		return nil
	}
	raw, err := h.auth.ListUsersAPI(ctx)
	if err != nil {
		logging.Warnf("resolveRegistrationRecipients: ListUsersAPI failed: %v", err)
		return nil
	}
	users, ok := raw.([]*auth.APIUser)
	if !ok {
		logging.Warnf("resolveRegistrationRecipients: unexpected ListUsersAPI result type %T", raw)
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, u := range users {
		if u == nil || !isAdminGroupMember(u.Groups) {
			continue
		}
		addr := strings.TrimSpace(u.Email)
		if addr == "" {
			continue
		}
		norm := strings.ToLower(addr)
		if seen[norm] {
			continue
		}
		seen[norm] = true
		out = append(out, addr)
	}
	return out
}

// isAdminGroupMember reports whether the given group IDs include the
// Administrators group, i.e. the user has full-access ({admin, *}) capability.
func isAdminGroupMember(groupIDs []string) bool {
	for _, g := range groupIDs {
		if g == auth.DefaultAdminGroupID {
			return true
		}
	}
	return false
}

// globalNotificationEmail loads the Settings → General notification email
// (best-effort: returns "" on transport error or when unset).
func (h *Handler) globalNotificationEmail(ctx context.Context) string {
	if h.config == nil {
		return ""
	}
	cfg, err := h.config.GetGlobalConfig(ctx)
	if err != nil || cfg == nil || cfg.NotificationEmail == nil {
		return ""
	}
	return strings.TrimSpace(*cfg.NotificationEmail)
}
