package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// MaxBoundIdentitiesPerGroup caps the recommendations one bound group may
// hold (issue #1746).
const MaxBoundIdentitiesPerGroup = 1000

// ErrInvalidBoundGroup marks a bound plan whose config groups conflict.
var ErrInvalidBoundGroup = errors.New("invalid bound plan group")

// EffectiveGroupID is the entry's group id: GroupID when set, otherwise its
// services map key (a legacy entry is its own one-entry group).
func (c *ServiceConfig) EffectiveGroupID(key string) string {
	if c.GroupID != "" {
		return c.GroupID
	}
	return key
}

// validateBoundGroups checks a bound_groups plan's entries against each other.
// Unmarked plans are skipped, so existing plans (including migration 000040's
// savings-plans siblings, which share one identity) validate as before. The
// marker-required and missing-binding rules are not here: they live in
// planstep.Precheck and the arming gate, so a de-escalating PATCH always
// passes.
func (p *PurchasePlan) validateBoundGroups() error {
	if p.RampSchedule.AllocationMode != RampAllocationBoundGroups {
		return nil
	}
	keys := make([]string, 0, len(p.Services))
	for k := range p.Services {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	groups := map[string]string{}     // effective group id -> key
	identities := map[string]string{} // provider|service|term|payment -> group
	bound := map[string]string{}      // bound identity -> group
	for _, k := range keys {
		svc := p.Services[k]
		if err := checkGroupKey(k, &svc); err != nil {
			return err
		}
		gid := svc.EffectiveGroupID(k)
		if prev, dup := groups[gid]; dup {
			return fmt.Errorf("%w: group %q is used by both %q and %q", ErrInvalidBoundGroup, gid, prev, k)
		}
		groups[gid] = k
		if err := checkGroupIdentity(&svc, gid, identities); err != nil {
			return err
		}
		if err := checkGroupBindings(&svc, gid, bound); err != nil {
			return err
		}
		if svc.MaxStepCommitment != nil {
			if _, err := StepCapCents(*svc.MaxStepCommitment); err != nil {
				return fmt.Errorf("%w: group %q: %w", ErrInvalidBoundGroup, gid, err)
			}
		}
	}
	return nil
}

// checkGroupIdentity refuses two enabled groups with the same provider,
// service, term and payment: their recommendations could not be told apart
// when sizing. ServiceConfig.Validate has already required each payment to be
// its provider's canonical token, so equal payments compare equal here.
func checkGroupIdentity(svc *ServiceConfig, gid string, seen map[string]string) error {
	if !svc.Enabled {
		return nil
	}
	id := fmt.Sprintf("%s|%s|%d|%s", svc.Provider, svc.Service, svc.Term, svc.Payment)
	if prev, dup := seen[id]; dup {
		return fmt.Errorf("%w: groups %q and %q have the same provider, service, term and payment", ErrInvalidBoundGroup, prev, gid)
	}
	seen[id] = gid
	return nil
}

// checkGroupBindings refuses a group with too many bound identities, or an
// identity bound twice, within the group or across groups.
func checkGroupBindings(svc *ServiceConfig, gid string, seen map[string]string) error {
	if len(svc.BoundIdentities) > MaxBoundIdentitiesPerGroup {
		return fmt.Errorf("%w: group %q binds %d recommendations (at most %d)", ErrInvalidBoundGroup, gid, len(svc.BoundIdentities), MaxBoundIdentitiesPerGroup)
	}
	for _, ident := range svc.BoundIdentities {
		if prev, dup := seen[ident]; dup {
			return fmt.Errorf("%w: recommendation %q is bound by group %q and group %q", ErrInvalidBoundGroup, ident, prev, gid)
		}
		seen[ident] = gid
	}
	return nil
}

// checkGroupKey requires a bound plan's entry to be keyed
// "provider/service#<group_id>" with that same GroupID, so the key and the
// field can never name different groups. The key's provider and service must
// also be the entry's own: the arming grant's scopes are derived from the key,
// while sizing buys by the entry's fields.
func checkGroupKey(key string, svc *ServiceConfig) error {
	prefix, suffix, ok := strings.Cut(key, BoundKeySeparator)
	if !ok || svc.GroupID == "" || suffix != svc.GroupID || !namesEntry(prefix, svc) {
		return fmt.Errorf("%w: entry %q must be keyed \"%s/%s%s%s\" (its own provider, service and group_id)", ErrInvalidBoundGroup, key, svc.Provider, svc.Service, BoundKeySeparator, svc.GroupID)
	}
	return nil
}

// namesEntry reports whether a key prefix is the entry's "provider/service" or
// "provider:service".
func namesEntry(prefix string, svc *ServiceConfig) bool {
	return prefix == svc.Provider+"/"+svc.Service || prefix == svc.Provider+":"+svc.Service
}
