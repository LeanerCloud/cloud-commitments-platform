/**
 * User list expand panel and inline group-membership toggle
 */

import type { APIUser } from '../../api';
import * as api from '../../api';
import { allUsers, availableGroups, setAllUsers } from '../state';
import { escapeHtml, showSuccess, showError, validateGroupCombination } from '../utils';
import { refreshCurrentUserAccess } from '../userActions';
import { ADMINISTRATORS_GROUP_ID } from '../../permissions';
import { permissionLabel, permissionTitle } from '../permissionMatrix';
import { confirmDialog } from '../../confirmDialog';
import { getCurrentUser } from '../../state';

// Placeholder row id withCurrentUser prepends when the API omits the caller.
export const SYNTHETIC_USER_ID = 'current';

/**
 * Look up a group's display name by ID. Falls back to the ID if the group
 * isn't in the loaded availableGroups list.
 */
export function groupName(groupId: string): string {
  const g = availableGroups.find(g => g.id === groupId);
  return g?.name ?? groupId;
}

function permissionBadge(perm: string): string {
  const i = perm.indexOf(':');
  const action = perm.slice(0, i);
  const resource = perm.slice(i + 1);
  return `<span class="permission-badge" title="${escapeHtml(permissionTitle(action, resource))}">${escapeHtml(permissionLabel(action, resource))}</span>`;
}

/**
 * Compute the effective permissions for a user from their group
 * memberships. Returns permissions as {action}:{resource} strings.
 *
 * PR #912 removed user.role; permissions now derive purely from the
 * union of the groups the user belongs to. availableGroups is loaded
 * on this admin-only page so we can resolve group permissions here.
 */
function effectivePermissions(user: APIUser): string[] {
  const perms = new Set<string>();

  user.groups.forEach(gid => {
    const g = availableGroups.find(gr => gr.id === gid);
    g?.permissions?.forEach(p => {
      if (p.action && p.resource) perms.add(`${p.action}:${p.resource}`);
    });
  });

  return Array.from(perms).sort();
}

/**
 * Render the expandable panel for a user row with inline group checkboxes
 * and an effective-permissions summary.
 */
export function renderUserExpandPanel(user: APIUser): string {
  const userGroups = new Set(user.groups);
  const perms = effectivePermissions(user);

  return `
    <div class="expand-panel-body">
      <div class="expand-panel-groups">
        <h5>Group Membership</h5>
        <p class="text-muted">A user receives the combined (union) of all selected groups&#8217; permissions, so adding a lower-privilege group alongside a higher one has no effect.</p>
        ${availableGroups.length === 0
          ? '<p class="text-muted">No groups defined yet.</p>'
          : `<div class="group-checkbox-list">
              ${availableGroups.map(g => `
                <label class="group-checkbox-label">
                  <input type="checkbox" class="group-assign-checkbox"
                    data-user-id="${escapeHtml(user.id)}"
                    data-group-id="${escapeHtml(g.id)}"
                    ${userGroups.has(g.id) ? 'checked' : ''} ${user.id === SYNTHETIC_USER_ID ? 'disabled' : ''}>
                  <span>${escapeHtml(g.name)}</span>
                </label>
              `).join('')}
            </div>`}
      </div>
      <div class="expand-panel-perms">
        <h5>Effective Permissions</h5>
        ${perms.length === 0
          ? '<p class="text-muted">No permissions.</p>'
          : `<div class="perm-badge-list">${perms.map(permissionBadge).join(' ')}</div>`}
      </div>
    </div>
  `;
}

/**
 * Toggle a group membership for a user inline (no modal).
 *
 * Before the API call, validates that the resulting group set is not a
 * contradictory combination (issue #1405: view-only + write-capable).
 *
 * After a successful API call, patch state in-memory and refresh only the
 * affected expand panel rather than re-rendering the whole table. This
 * prevents the panel from collapsing on every toggle (issue #998) and
 * keeps the user in context for subsequent group changes.
 */
export async function toggleUserGroup(userId: string, groupId: string, checked: boolean): Promise<void> {
  const user = allUsers.find(u => u.id === userId);
  if (!user) return;

  const group = availableGroups.find(g => g.id === groupId);
  const groupLabel = group ? group.name : groupId;

  const nextFor = (u: APIUser): string[] => (checked
    ? [...new Set([...u.groups, groupId])]
    : u.groups.filter(g => g !== groupId));
  const next = nextFor(user);

  // Validate the resulting group combination before hitting the API.
  // A view-only group (all permissions are view:*) combined with a
  // write-capable group is semantically contradictory (issue #1405).
  const revertCheckbox = (): void => {
    const cb = document.querySelector<HTMLInputElement>(
      `.group-assign-checkbox[data-user-id="${CSS.escape(userId)}"][data-group-id="${CSS.escape(groupId)}"]`,
    );
    if (cb) cb.checked = !checked;
  };
  const combinationError = validateGroupCombination(next, availableGroups);
  if (combinationError) {
    showError(combinationError);
    revertCheckbox();
    return;
  }

  const selfDemotion = !checked
    && groupId === ADMINISTRATORS_GROUP_ID
    && user.id === getCurrentUser()?.id;
  const ok = await confirmDialog({
    title: checked
      ? `Add ${user.email} to "${groupLabel}"?`
      : `Remove ${user.email} from "${groupLabel}"?`,
    body: selfDemotion
      ? 'You are removing yourself from Administrators. You will lose access to administration, and another administrator must restore it.'
      : checked
        ? `${user.email} will gain the permissions of the "${groupLabel}" group.`
        : `${user.email} will lose the permissions of the "${groupLabel}" group.`,
    confirmLabel: checked ? 'Add to group' : 'Remove from group',
    destructive: !checked,
  });
  if (!ok) {
    revertCheckbox();
    return;
  }

  // A concurrent toggle may have changed the user while the dialog was open.
  const latest = allUsers.find(u => u.id === userId) ?? user;
  const groupsToSave = nextFor(latest);
  const latestError = validateGroupCombination(groupsToSave, availableGroups);
  if (latestError) {
    showError(latestError);
    revertCheckbox();
    return;
  }

  try {
    await api.updateUser(userId, { groups: groupsToSave });

    // Patch the user in-memory so subsequent renders and effectivePermissions
    // calculations reflect the change without a full round-trip re-render.
    const updatedUser: APIUser = { ...latest, groups: groupsToSave };
    setAllUsers(allUsers.map(u => (u.id === userId ? updatedUser : u)));
    void refreshCurrentUserAccess([userId]); // self-edits re-gate the UI (#20)

    // Re-render the expand panel in place so the checkboxes and permissions
    // summary stay current without collapsing the row or re-rendering the
    // whole table (which would tear down all open panels).
    const panel = document.querySelector<HTMLElement>(
      `.user-expand-panel[data-user-id="${CSS.escape(userId)}"]`,
    );
    // innerHTML is intentional: renderUserExpandPanel returns server-derived HTML that
    // is already sanitized upstream; no unsanitized user-controlled input reaches here.
    if (panel) panel.innerHTML = renderUserExpandPanel(updatedUser);

    // Refresh the group-badge cell in the main row so it reflects the new
    // membership without a full re-render.
    const groupsCell = document.querySelector<HTMLElement>(
      `tr.user-row[data-user-id="${CSS.escape(userId)}"] .groups-cell`,
    );
    if (groupsCell) {
      groupsCell.innerHTML = updatedUser.groups.length > 0
        ? updatedUser.groups.map(g => `<span class="badge badge-group">${escapeHtml(groupName(g))}</span>`).join(' ')
        : '<span class="text-muted">No groups</span>';
    }

    // Surface inline-save confirmation so users know the toggle stuck.
    showSuccess(checked
      ? `Added ${user.email} to "${groupLabel}"`
      : `Removed ${user.email} from "${groupLabel}"`);
  } catch (error) {
    const err = error as Error;
    console.error('Failed to update user groups:', err);
    showError(`Failed to update group membership: ${err.message}`);
    // Revert the checkbox
    const cb = document.querySelector<HTMLInputElement>(
      `.group-assign-checkbox[data-user-id="${CSS.escape(userId)}"][data-group-id="${CSS.escape(groupId)}"]`,
    );
    if (cb) cb.checked = !checked;
  }
}
