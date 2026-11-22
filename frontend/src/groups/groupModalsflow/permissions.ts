/**
 * Group permission form: rendering, collection and constraint validation.
 */

import type { Permission } from '../../api';
import { escapeHtml } from '../../users/utils';
import { iconSvg } from '../../icons';
import { clearNoPermissionsProblem, type GroupFieldProblem } from '../groupFormValidation';
import { buildActionOptions, buildResourceOptions } from '../permissionOptions';

// Messages shown under the field for the saveGroup refusals that follow the
// blank-field checks (issue #727). The toasts keep the row-naming summary.
export const NO_PERMISSIONS_FIELD_MESSAGE = 'Add at least one permission. To remove all privileges, delete the group instead.';
const STORED_VALUE_FIELD_MESSAGE = 'This stored value cannot be shown in this form, so saving would drop the restriction. '
  + 'Repair it through the API or in the database, then reload this page.';
const EMPTY_LIST_FIELD_MESSAGE = 'This list has no usable entries. Enter at least one value, or clear the box for no restriction.';
const MAX_AMOUNT_FIELD_MESSAGE = 'Enter an amount greater than 0, or leave it blank for no limit.';

function constraintInput(item: Element, dimension: string): HTMLInputElement | null {
  return item.querySelector<HTMLInputElement>(`.perm-${dimension}`);
}

/**
 * Add a new permission to the form
 */
export function addPermission(permission?: Permission): void {
  const permissionsList = document.getElementById('permissions-list');
  if (!permissionsList) return;

  const permDiv = document.createElement('div');
  permDiv.className = 'permission-item';

  // Record the verdict, not the value: which of this row's constraint lists
  // hold something the text encoding cannot carry back unchanged. saveGroup
  // refuses while any row carries this. The attribute lives and dies with the
  // row, so removing the row clears it and nothing outlives the modal. The
  // action:resource label is read live from the selects at refusal time rather
  // than stored here, so it cannot go stale against what the operator sees.
  const unsafe = unrepresentableDimensions(permission?.constraints);
  if (unsafe.length > 0) {
    permDiv.setAttribute('data-unrepresentable', unsafe.join(', '));
  }

  permDiv.innerHTML = `
    <div class="form-row">
      <label>Action:
        <select class="perm-action" required>
          ${buildActionOptions(permission?.action)}
        </select>
      </label>
      <label>Resource:
        <select class="perm-resource" required>
          ${buildResourceOptions(permission?.resource)}
        </select>
      </label>
      <button type="button" class="btn-small btn-danger icon-btn remove-permission-btn" aria-label="Remove permission" title="Remove permission">${iconSvg('trash-2')}</button>
    </div>
    <div class="constraints-section">
      <h4>Constraints (Optional)</h4>
      <div class="form-row">
        <label>Cloud Account IDs (comma-separated):
          <input type="text" class="perm-accounts" value="${escapeHtml(permission?.constraints?.accounts?.join(', ') || '')}" placeholder="2f1c8e40-...-uuid, unattributed">
        </label>
      </div>
      <div class="form-row">
        <label>Providers (comma-separated):
          <input type="text" class="perm-providers" value="${escapeHtml(permission?.constraints?.providers?.join(', ') || '')}" placeholder="aws, azure, gcp">
        </label>
        <label>Services (comma-separated):
          <input type="text" class="perm-services" value="${escapeHtml(permission?.constraints?.services?.join(', ') || '')}" placeholder="ec2, rds">
        </label>
      </div>
      <div class="form-row">
        <label>Regions (comma-separated):
          <input type="text" class="perm-regions" value="${escapeHtml(permission?.constraints?.regions?.join(', ') || '')}" placeholder="us-east-1, us-west-2">
        </label>
        <label>Max Amount ($):
          <input type="number" class="perm-max-amount" value="${escapeHtml(String(permission?.constraints?.max_amount ?? ''))}" placeholder="10000" min="0">
        </label>
      </div>
    </div>
  `;

  permissionsList.appendChild(permDiv);
  clearNoPermissionsProblem();

  // Add event listener for remove button
  const removeBtn = permDiv.querySelector('.remove-permission-btn');
  if (removeBtn) {
    removeBtn.addEventListener('click', () => {
      permDiv.remove();
    });
  }
}

// The one tokenizer for every comma-separated constraint input. Both the save
// path and the render-time safety check below go through it, deliberately: if
// this drops or alters a value, the check must catch it, and sharing the
// function is what stops the two from drifting apart.
function parseConstraintList(raw: string): string[] {
  return raw.split(',').map(s => s.trim()).filter(s => s);
}

// Names the constraint lists in `constraints` that this form cannot carry
// back unchanged (issue #1629, raised again by CodeRabbit on PR #1875).
//
// The form encodes a list as comma-separated text, and that encoding is not
// injective. A stored [""] renders blank and re-parses as ABSENT; a stored
// [","] re-parses as an empty list; a stored [" acct A "] comes back trimmed,
// which is a different fence, since enforcement compares values exactly. In
// every case the form would re-submit a materially different restriction than
// the one it loaded, and for a constraint list "different" means WIDER: an
// empty list is "no restriction on this dimension" at enforcement
// (matchStringListConstraints). The refusal in saveGroup is the loud
// alternative to that silent widening.
//
// The test is a round trip through parseConstraintList rather than a
// hand-written blank check, for two reasons: it is the same predicate the
// save path uses, so the two cannot disagree; and a per-entry "is it blank"
// test would MISS [","], whose entry is not blank yet still vanishes, because
// the split runs before the filter.
//
// An absent or empty list is NOT flagged. Both mean "no restriction on this
// dimension", both are perfectly normal, and refusing them would make
// ordinary unconstrained groups uneditable.
// The constraint dimensions the form renders as comma-separated text, each in
// an input classed `perm-<dimension>`. One list, so the render-time check and
// the typed-input check below cannot cover different sets.
const LIST_CONSTRAINT_DIMENSIONS = ['accounts', 'providers', 'services', 'regions'] as const;

function unrepresentableDimensions(constraints: Permission['constraints']): string[] {
  if (!constraints) return [];

  const unsafe: string[] = [];
  for (const dimension of LIST_CONSTRAINT_DIMENSIONS) {
    const values = constraints[dimension];
    if (!values || values.length === 0) continue;
    const reparsed = parseConstraintList(values.join(', '));
    if (reparsed.length !== values.length || reparsed.some((value, i) => value !== values[i])) {
      unsafe.push(dimension);
    }
  }
  return unsafe;
}

// Every reason this form must refuse to save, as one message per problem,
// naming the permission by index and by action:resource and naming the
// constraint list. Mirrors the specificity of the backend's own refusal in
// validateConstraintEntries.
//
// Two directions, one rule and one error path, because they are the same
// defect: the form would send a constraint list that says something different
// from what the operator is looking at, and for a constraint list "different"
// is WIDER, since an empty list is "no restriction on this dimension" at
// enforcement.
//
//  1. STORED: the loaded value cannot survive the text encoding (flagged when
//     the row rendered, see unrepresentableDimensions).
//  2. TYPED: the box holds something, but parseConstraintList reduces it to
//     nothing -- "," or " , " being the reachable case, since it takes only a
//     stray comma. Without this the save would send an empty list and silently
//     remove the fence.
//
// Both use parseConstraintList itself as the predicate, never a second
// implementation of "parses to nothing" that could drift from it.
//
// A BLANK box is not an error: it means "no restriction on this dimension",
// which is ordinary. Blank is measured after trimming, so a box holding only
// spaces counts as blank -- it is indistinguishable from empty on screen, and
// erroring on a field that looks empty would be unfixable from the UI.
export function unrepresentablePermissionErrors(): GroupFieldProblem[] {
  const permissionsList = document.getElementById('permissions-list');
  if (!permissionsList) return [];

  const errors: GroupFieldProblem[] = [];
  permissionsList.querySelectorAll('.permission-item').forEach((item, index) => {
    const action = (item.querySelector('.perm-action') as HTMLSelectElement | null)?.value || '';
    const resource = (item.querySelector('.perm-resource') as HTMLSelectElement | null)?.value || '';
    const label = `permission ${index} (${action}:${resource})`;

    const stored = (item.getAttribute('data-unrepresentable') || '').split(', ').filter(d => d);
    if (stored.length > 0) {
      const summary = `${label} has a stored "${stored.join(', ')}" constraint value this form cannot represent`;
      // Every affected input gets the inline note; the toast names the row once.
      stored.forEach((dimension, i) => errors.push({
        control: constraintInput(item, dimension) ?? (item as HTMLElement),
        message: STORED_VALUE_FIELD_MESSAGE,
        summary: i === 0 ? summary : '',
      }));
    }

    for (const dimension of LIST_CONSTRAINT_DIMENSIONS) {
      // Already refused for this row on the stored value; do not say it twice.
      if (stored.includes(dimension)) continue;
      const control = constraintInput(item, dimension);
      const raw = control?.value ?? '';
      if (control && raw.trim() !== '' && parseConstraintList(raw).length === 0) {
        errors.push({ control, message: EMPTY_LIST_FIELD_MESSAGE, summary: `${label} has a "${dimension}" value with no usable entries` });
      }
    }
  });
  return errors;
}

// Max Amount is typed by the admin, unlike the stored-value problems above, so
// its message tells them how to fix the field instead of pointing at the API.
// 0 would read as "no cap" at enforcement, so it is refused rather than sent.
export function maxAmountInputErrors(): GroupFieldProblem[] {
  const errors: GroupFieldProblem[] = [];
  document.querySelectorAll('#permissions-list .permission-item').forEach((item, index) => {
    const input = item.querySelector('.perm-max-amount') as HTMLInputElement | null;
    if (!input) return;
    const problem = { control: input, message: MAX_AMOUNT_FIELD_MESSAGE, summary: `permission ${index}` };
    // A number input reports unparsable text ("abc", "1e") as value "", which
    // would read as blank and save with no cap; badInput is the only signal.
    if (input.validity?.badInput) {
      errors.push(problem);
      return;
    }
    const raw = input.value;
    if (raw.trim() === '') return;
    const amount = Number(raw);
    if (!Number.isFinite(amount) || amount <= 0) {
      errors.push(problem);
    }
  });
  return errors;
}

/**
 * Render permissions list
 */
export function renderPermissions(permissions: Permission[]): void {
  const permissionsList = document.getElementById('permissions-list');
  if (!permissionsList) return;

  permissionsList.innerHTML = '';

  if (permissions.length === 0) {
    addPermission();
  } else {
    permissions.forEach(perm => addPermission(perm));
  }
}

/**
 * Collect permissions from form
 */
export function collectPermissions(): Permission[] {
  const permissionsList = document.getElementById('permissions-list');
  if (!permissionsList) return [];

  const permissions: Permission[] = [];
  const items = permissionsList.querySelectorAll('.permission-item');

  items.forEach(item => {
    const action = (item.querySelector('.perm-action') as HTMLSelectElement)?.value;
    const resource = (item.querySelector('.perm-resource') as HTMLSelectElement)?.value;

    if (!action || !resource) return;

    const permission: Permission = { action, resource };

    // Collect constraints. Dropping `accounts` here WIDENS the permission
    // rather than merely losing data: an empty AccountIDs list means "no
    // restriction on this dimension" at enforcement
    // (matchStringListConstraints), so a permission scoped to one cloud
    // account came back out of a cosmetic rename scoped to all of them
    // (issue #1629).
    const accounts = (item.querySelector('.perm-accounts') as HTMLInputElement)?.value;
    const providers = (item.querySelector('.perm-providers') as HTMLInputElement)?.value;
    const services = (item.querySelector('.perm-services') as HTMLInputElement)?.value;
    const regions = (item.querySelector('.perm-regions') as HTMLInputElement)?.value;
    const maxAmount = (item.querySelector('.perm-max-amount') as HTMLInputElement)?.value;

    if (accounts || providers || services || regions || maxAmount) {
      permission.constraints = {};
      // Attach a dimension only when it yields at least one entry, so a box
      // that LOOKS empty produces the same payload as one that IS empty. An
      // input holding only whitespace would otherwise send [], which means
      // the same thing at enforcement but makes the request depend on
      // invisible characters. A box that parses to nothing while holding
      // something visible never reaches here: saveGroup already refused it.
      const accountsList = parseConstraintList(accounts || '');
      const providersList = parseConstraintList(providers || '');
      const servicesList = parseConstraintList(services || '');
      const regionsList = parseConstraintList(regions || '');
      if (accountsList.length > 0) permission.constraints.accounts = accountsList;
      if (providersList.length > 0) permission.constraints.providers = providersList;
      if (servicesList.length > 0) permission.constraints.services = servicesList;
      if (regionsList.length > 0) permission.constraints.regions = regionsList;
      if ((maxAmount ?? '').trim() !== '') permission.constraints.max_amount = Number(maxAmount);
      // Nothing survived: the permission carries no restriction, so send no
      // constraints object rather than an empty one.
      if (Object.keys(permission.constraints).length === 0) {
        delete permission.constraints;
      }
    }

    permissions.push(permission);
  });

  return permissions;
}
