import { reflectDirtyState } from '../settings-subnav';
import { getFieldValue } from './settings-fields';
import { TRACKED_FIELDS, savedSnapshot } from './settings-state';

export function snapshotAllFields(): void {
  TRACKED_FIELDS.forEach(id => { savedSnapshot[id] = getFieldValue(id); });
}

export function updateDirtyMarkers(): void {
  let anyDirty = false;
  TRACKED_FIELDS.forEach(id => {
    const el = document.getElementById(id);
    if (!el) return;
    const dirty = getFieldValue(id) !== savedSnapshot[id];
    if (dirty) anyDirty = true;
    if (el instanceof HTMLInputElement && el.type === 'checkbox') {
      // Highlight the containing setting-input div for checkboxes
      el.closest<HTMLElement>('.setting-input')?.classList.toggle('dirty', dirty);
    } else {
      el.classList.toggle('dirty', dirty);
    }
  });
  // Surface aggregate dirty state to the Settings top-tab (badge dot) and
  // the sticky save bar (toggle "Unsaved changes" affordance).
  reflectDirtyState(anyDirty);
}

/**
 * Returns true if any tracked field has been changed since the last save/load.
 *
 * Returns false when no snapshot has been taken yet (settings are still
 * loading or have never loaded successfully). Before the first
 * snapshotAllFields() call, savedSnapshot is an empty object, and comparing
 * any live DOM value against undefined would always return true -- causing
 * spurious "unsaved changes" prompts for users who navigate away before the
 * async settings load completes (issue #1441).
 */
export function isUnsavedChanges(): boolean {
  // savedSnapshot starts as {} and is populated by snapshotAllFields() only
  // after loadGlobalSettings() resolves. If it is still empty the form is
  // still loading and no edit could have been made yet.
  if (Object.keys(savedSnapshot).length === 0) return false;
  return TRACKED_FIELDS.some(id => getFieldValue(id) !== savedSnapshot[id]);
}

export function setupDirtyTracking(signal?: AbortSignal): void {
  TRACKED_FIELDS.forEach(id => {
    const el = document.getElementById(id);
    if (!el) return;
    el.addEventListener('change', () => updateDirtyMarkers(), { signal });
    // Also listen to input for text fields so highlighting is immediate
    if (el instanceof HTMLInputElement && el.type !== 'checkbox') {
      el.addEventListener('input', () => updateDirtyMarkers(), { signal });
    }
  });
}
