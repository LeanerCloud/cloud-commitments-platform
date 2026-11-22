/**
 * User list rendering functionality
 */

import type { APIUser } from '../api';
import {
  allUsers,
  filteredUsers,
  selectedUserIds,
  availableGroups,
  addSelectedUserId,
  removeSelectedUserId,
  clearSelectedUserIds,
} from './state';
import { escapeHtml, formatRelativeTime, formatDate } from './utils';
import { openEditUserModal, deleteUser } from './userActions';
import { ADMINISTRATORS_GROUP_ID } from '../permissions';
import { SYNTHETIC_USER_ID, groupName, renderUserExpandPanel, toggleUserGroup } from './userListflow/expandPanel';
import { createIcon, iconSvg } from '../icons';

/**
 * Single AbortController for the document-level group-assign-checkbox
 * change listener. Replaced on every renderUsers() call so only one
 * listener is ever active, preventing duplicate toasts (issue #998).
 */
let groupAssignListenerCtrl: AbortController | null = null;

/**
 * Render user statistics
 */
export function renderUserStats(): void {
  const statsContainer = document.getElementById('user-stats');
  if (!statsContainer) return;

  const totalUsers = allUsers.length;
  // Count administrators by group membership (PR #912: role dropped).
  const adminUsers = allUsers.filter(u =>
    Array.isArray(u.groups) && u.groups.includes(ADMINISTRATORS_GROUP_ID)
  ).length;
  const mfaEnabled = allUsers.filter(u => u.mfa_enabled).length;
  const showing = filteredUsers.length;

  statsContainer.innerHTML = `
    <div class="stats-grid">
      <div class="stat-card">
        <div class="stat-value">${totalUsers}</div>
        <div class="stat-label">Total Users</div>
      </div>
      <div class="stat-card">
        <div class="stat-value">${adminUsers}</div>
        <div class="stat-label">Administrators</div>
      </div>
      <div class="stat-card">
        <div class="stat-value">${mfaEnabled}</div>
        <div class="stat-label">MFA Enabled</div>
      </div>
      <div class="stat-card ${showing !== totalUsers ? 'stat-card-highlight' : ''}">
        <div class="stat-value">${showing}</div>
        <div class="stat-label">Showing</div>
      </div>
    </div>
  `;
}

/**
 * Render users list with enhanced design
 */
export function renderUsers(users: APIUser[]): void {
  const container = document.getElementById('users-list');
  if (!container) return;

  if (users.length === 0) {
    container.innerHTML = `<div class="empty">${allUsers.length === 0 // #182: no users at all vs filtered out
      ? 'No users yet. Use Create user to give someone access.' : 'No users found matching your filters'}</div>`;
    return;
  }

  const selectableCount = users.filter(u => u.id !== SYNTHETIC_USER_ID).length;
  const table = `
    <table class="data-table users-table">
      <thead>
        <tr>
          <th width="40">
            <input type="checkbox" id="select-all-users" aria-label="Select all users" ${selectableCount === 0 ? 'disabled' : ''} ${selectedUserIds.size > 0 && selectedUserIds.size === selectableCount ? 'checked' : ''}>
          </th>
          <th width="32"></th>
          <th>Email</th>
          <th>Groups</th>
          <th>MFA</th>
          <th>Created</th>
          <th>Last Login</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
        ${users.map(user => `
          <tr class="user-row ${selectedUserIds.has(user.id) ? 'row-selected' : ''}" data-user-id="${escapeHtml(user.id)}">
            <td>
              <input type="checkbox" class="user-checkbox" data-user-id="${escapeHtml(user.id)}" aria-label="Select ${escapeHtml(user.email)}" ${selectedUserIds.has(user.id) ? 'checked' : ''} ${user.id === SYNTHETIC_USER_ID ? 'disabled' : ''}>
            </td>
            <td>
              <button class="btn-small btn-icon user-expand-btn" data-user-id="${escapeHtml(user.id)}" title="Expand" aria-label="Expand user ${escapeHtml(user.email)}" aria-expanded="false">${iconSvg('chevron-right')}</button>
            </td>
            <td>
              <div class="user-email">
                <strong>${escapeHtml(user.email)}</strong>
                ${user.id === SYNTHETIC_USER_ID ? '<span class="badge badge-info">You</span>' : ''}
              </div>
            </td>
            <td>
              <div class="groups-cell">
                ${user.groups.length > 0 ? user.groups.map(g => `<span class="badge badge-group">${escapeHtml(groupName(g))}</span>`).join(' ') : '<span class="text-muted">No groups</span>'}
              </div>
            </td>
            <td>
              ${user.mfa_enabled
                ? '<span class="badge badge-success"><i class="icon-shield"></i> Enabled</span>'
                : '<span class="badge badge-warning"><i class="icon-shield-off"></i> Disabled</span>'}
            </td>
            <td class="nowrap"><span class="text-muted">${user.created_at ? formatDate(user.created_at) : '-'}</span></td>
            <td class="nowrap"><span class="text-muted">${user.last_login ? formatRelativeTime(user.last_login) : 'Never'}</span></td>
            <td>
              <div class="action-buttons row-actions">
                <button class="btn-small icon-btn edit-user-btn" data-user-id="${escapeHtml(user.id)}" ${user.id === SYNTHETIC_USER_ID ? 'disabled' : ''} aria-label="Edit user ${escapeHtml(user.email)}" title="Edit user">${iconSvg('edit')}</button>
                <button class="btn-small btn-danger icon-btn delete-user-btn" data-user-id="${escapeHtml(user.id)}" ${user.id === SYNTHETIC_USER_ID ? 'disabled' : ''} aria-label="Delete user ${escapeHtml(user.email)}" title="Delete user">${iconSvg('trash-2')}</button>
              </div>
            </td>
          </tr>
          <tr class="user-expand-row hidden" data-user-id="${escapeHtml(user.id)}">
            <td colspan="8">
              <div class="user-expand-panel" data-user-id="${escapeHtml(user.id)}"></div>
            </td>
          </tr>
        `).join('')}
      </tbody>
    </table>
  `;

  container.innerHTML = table;

  // Setup event listeners
  setupUserTableListeners();
}


/**
 * Setup event listeners for user table
 */
function setupUserTableListeners(): void {
  // Select all checkbox
  // Patch all rows in-place instead of calling renderUsers(), which would
  // tear down and rebuild the entire table and collapse any open expand
  // panels (issue #1404 symptom: expand arrow / panel lost on checkbox click).
  const selectAllCheckbox = document.getElementById('select-all-users') as HTMLInputElement;
  if (selectAllCheckbox) {
    selectAllCheckbox.addEventListener('change', (e) => {
      const checked = (e.target as HTMLInputElement).checked;
      if (checked) {
        filteredUsers.filter(u => u.id !== SYNTHETIC_USER_ID).forEach(user => addSelectedUserId(user.id));
      } else {
        clearSelectedUserIds();
      }
      // Patch each row's selected class and its individual checkbox without
      // re-rendering the whole table (preserves open expand panels).
      filteredUsers.filter(u => u.id !== SYNTHETIC_USER_ID).forEach(user => {
        const row = document.querySelector<HTMLElement>(
          `tr.user-row[data-user-id="${CSS.escape(user.id)}"]`,
        );
        const cb = document.querySelector<HTMLInputElement>(
          `.user-checkbox[data-user-id="${CSS.escape(user.id)}"]`,
        );
        if (row) {
          if (checked) row.classList.add('row-selected');
          else row.classList.remove('row-selected');
        }
        if (cb) cb.checked = checked;
      });
      updateBulkActionsBar();
    });
  }

  // Individual checkboxes
  // Same rationale: patch the affected row in-place instead of calling
  // renderUsers() so open expand panels are not collapsed (issue #1404).
  document.querySelectorAll('.user-checkbox').forEach(checkbox => {
    checkbox.addEventListener('change', (e) => {
      const userId = (e.target as HTMLElement).dataset.userId;
      if (!userId) return;

      const nowChecked = (e.target as HTMLInputElement).checked;
      if (nowChecked) {
        addSelectedUserId(userId);
      } else {
        removeSelectedUserId(userId);
      }

      // Toggle the row-selected class on just the affected row.
      const row = document.querySelector<HTMLElement>(
        `tr.user-row[data-user-id="${CSS.escape(userId)}"]`,
      );
      if (row) {
        if (nowChecked) row.classList.add('row-selected');
        else row.classList.remove('row-selected');
      }

      // Keep the select-all checkbox in sync.
      const selectAll = document.getElementById('select-all-users') as HTMLInputElement | null;
      if (selectAll) {
        selectAll.checked =
          selectedUserIds.size > 0 &&
          selectedUserIds.size === filteredUsers.filter(u => u.id !== SYNTHETIC_USER_ID).length;
      }

      updateBulkActionsBar();
    });
  });

  // Edit buttons
  document.querySelectorAll('.edit-user-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      const userId = (btn as HTMLElement).dataset.userId;
      if (userId) void openEditUserModal(userId);
    });
  });

  // Delete buttons
  document.querySelectorAll('.delete-user-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      const userId = (btn as HTMLElement).dataset.userId;
      if (userId) void deleteUser(userId);
    });
  });

  // Row expand toggle: click to expand/collapse a row and populate the
  // inline panel with group checkboxes + effective permissions.
  document.querySelectorAll('.user-expand-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      const userId = (btn as HTMLElement).dataset.userId;
      if (!userId) return;
      const expandRow = document.querySelector<HTMLElement>(
        `tr.user-expand-row[data-user-id="${CSS.escape(userId)}"]`,
      );
      const panel = document.querySelector<HTMLElement>(
        `.user-expand-panel[data-user-id="${CSS.escape(userId)}"]`,
      );
      if (!expandRow || !panel) return;

      const isHidden = expandRow.classList.contains('hidden');
      if (isHidden) {
        const user = allUsers.find(u => u.id === userId);
        // innerHTML is intentional: renderUserExpandPanel returns server-derived HTML that
        // is already sanitized upstream; no unsanitized user-controlled input reaches here.
        if (user) panel.innerHTML = renderUserExpandPanel(user);
        expandRow.classList.remove('hidden');
        btn.setAttribute('aria-expanded', 'true');
        btn.replaceChildren(createIcon('chevron-down'));
      } else {
        expandRow.classList.add('hidden');
        btn.setAttribute('aria-expanded', 'false');
        btn.replaceChildren(createIcon('chevron-right'));
      }
    });
  });

  // Inline group assignment checkboxes inside the expand panel.
  // Use AbortController so only one listener is ever registered on document,
  // regardless of how many times renderUsers() is called. Without this,
  // every render stacks another listener and triggers multiple toasts per
  // toggle (issue #998).
  groupAssignListenerCtrl?.abort();
  groupAssignListenerCtrl = new AbortController();
  document.addEventListener('change', (e) => {
    const target = e.target as HTMLElement;
    if (!target.classList.contains('group-assign-checkbox')) return;
    const userId = target.dataset.userId;
    const groupId = target.dataset.groupId;
    if (!userId || !groupId) return;
    void toggleUserGroup(userId, groupId, (target as HTMLInputElement).checked);
  }, { signal: groupAssignListenerCtrl.signal });
}

/**
 * Update bulk actions bar visibility and selection count.
 */
export function updateBulkActionsBar(): void {
  const bulkBar = document.getElementById('bulk-actions-bar');
  if (!bulkBar) return;

  const selectedCount = selectedUserIds.size;

  if (selectedCount === 0) {
    bulkBar.classList.add('hidden');
  } else {
    bulkBar.classList.remove('hidden');
    const countEl = document.getElementById('selected-count');
    if (countEl) countEl.textContent = selectedCount.toString();
  }
}

/**
 * Populate the #bulk-group-select dropdown with the currently loaded groups.
 * Called by loadUsers after groups are refreshed so the options stay in sync.
 */
export function populateBulkGroupSelect(): void {
  const select = document.getElementById('bulk-group-select') as HTMLSelectElement | null;
  if (!select) return;
  while (select.options.length > 1) select.remove(1);
  for (const group of availableGroups) {
    const opt = document.createElement('option');
    opt.value = group.id;
    opt.textContent = group.name;
    select.add(opt);
  }
}
