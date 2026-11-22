import * as state from '../state';

/**
 * Mount the "Amortize upfront over term" checkbox into a container element
 * (idempotent -- safe to call on every loadHistory).
 *
 * The checkbox is wired to setAmortizeUpfront so a change here is reflected
 * in all other views via the shared localStorage key + subscriber pattern.
 */
export function mountAmortizeCheckbox(containerId: string, checkboxId: string): void {
  const container = document.getElementById(containerId);
  if (!container) return;
  if (document.getElementById(checkboxId)) return; // already mounted

  const wrapper = document.createElement('label');
  wrapper.className = 'amortize-toggle-label';
  wrapper.htmlFor = checkboxId;

  const cb = document.createElement('input');
  cb.type = 'checkbox';
  cb.id = checkboxId;
  cb.checked = state.getAmortizeUpfront();
  cb.addEventListener('change', () => {
    state.setAmortizeUpfront(cb.checked);
  });

  wrapper.appendChild(cb);
  wrapper.appendChild(document.createTextNode(' Amortize upfront over term'));
  container.appendChild(wrapper);
}

/** Keep an already-mounted checkbox in sync when state changes externally. */
export function syncAmortizeCheckbox(checkboxId: string): void {
  const cb = document.getElementById(checkboxId) as HTMLInputElement | null;
  if (cb) cb.checked = state.getAmortizeUpfront();
}
