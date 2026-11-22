import * as state from '../state';
import { TOGGLEABLE_COLUMNS, saveColumnVisibility } from './columnPrefs';
import { positionPopover } from './popoverContent';
import { closePopover, rerenderRecommendations } from './popover';

//
// Column-visibility popover (issue #318)
//
// Separate state from the column-filter popover (openPopover / outsideClickHandler
// etc.) to avoid conflating the two interactions.  Shares the positionPopover()
// helper for positioning.
//

interface VisibilityPopoverState {
  el: HTMLDivElement;
  checkboxes: Map<state.RecommendationsColumnId, HTMLInputElement>;
  trigger: HTMLElement;
}

let openVisibilityPopover: VisibilityPopoverState | null = null;
let visOutsideClickHandler: ((e: MouseEvent) => void) | null = null;
let visEscKeyHandler: ((e: KeyboardEvent) => void) | null = null;

export function closeVisibilityPopover(): void {
  if (!openVisibilityPopover) return;
  const { el, trigger } = openVisibilityPopover;
  el.remove();
  trigger.setAttribute('aria-expanded', 'false');
  openVisibilityPopover = null;
  if (visOutsideClickHandler) {
    document.removeEventListener('mousedown', visOutsideClickHandler);
    visOutsideClickHandler = null;
  }
  if (visEscKeyHandler) {
    document.removeEventListener('keydown', visEscKeyHandler);
    visEscKeyHandler = null;
  }
}

function openVisibilityPopover_(anchor: HTMLElement): void {
  // Close any open filter popover when visibility popover opens (only one popover
  // at a time keeps the UI from getting cluttered).
  closePopover();

  if (openVisibilityPopover) {
    closeVisibilityPopover();
    return; // toggle: second click on the button closes the popover
  }

  const popover = document.createElement('div');
  popover.className = 'column-visibility-popover';
  popover.setAttribute('role', 'dialog');
  popover.setAttribute('aria-label', 'Show/hide columns');
  popover.style.display = 'none';
  document.body.appendChild(popover);

  const title = document.createElement('p');
  title.className = 'column-visibility-title';
  title.textContent = 'Show/hide columns';
  popover.appendChild(title);

  const hidden = state.getHiddenColumns();
  const checkboxes = new Map<state.RecommendationsColumnId, HTMLInputElement>();

  for (const col of TOGGLEABLE_COLUMNS) {
    const row = document.createElement('label');
    row.className = 'column-visibility-row';

    const cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.checked = !hidden.has(col.key);
    cb.setAttribute('aria-label', col.label);
    checkboxes.set(col.key, cb);

    cb.addEventListener('change', () => {
      const currentHidden = new Set(state.getHiddenColumns());
      if (cb.checked) {
        currentHidden.delete(col.key);
      } else {
        currentHidden.add(col.key);
      }
      state.setHiddenColumns(currentHidden);
      saveColumnVisibility(currentHidden);
      rerenderRecommendations();
    });

    row.appendChild(cb);
    row.appendChild(document.createTextNode(' ' + col.label));
    popover.appendChild(row);
  }

  openVisibilityPopover = { el: popover, checkboxes, trigger: anchor };
  anchor.setAttribute('aria-expanded', 'true');
  positionPopover(popover, anchor);

  // Keyboard: Escape closes.
  visEscKeyHandler = (e: KeyboardEvent): void => {
    if (e.key === 'Escape') {
      closeVisibilityPopover();
      anchor.focus();
    }
  };
  document.addEventListener('keydown', visEscKeyHandler);

  // Click-outside closes.
  visOutsideClickHandler = (e: MouseEvent): void => {
    const target = e.target as Node;
    if (!popover.contains(target) && !anchor.contains(target)) {
      closeVisibilityPopover();
    }
  };
  // Defer one tick so the current click event (which opened the popover) doesn't
  // immediately close it via the click-outside handler.
  const handler = visOutsideClickHandler;
  setTimeout(() => {
    if (handler && openVisibilityPopover && visOutsideClickHandler === handler) {
      document.addEventListener('mousedown', handler);
    }
  }, 0);
}

// mountColumnsButton mounts the "Columns ▾" button in the filter-status bar once
// and updates its label on subsequent calls to reflect hidden-column count.
export function mountColumnsButton(bar: HTMLElement): void {
  let btn = bar.querySelector<HTMLButtonElement>('.column-visibility-btn');
  if (!btn) {
    btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'column-visibility-btn';
    btn.setAttribute('aria-haspopup', 'dialog');
    btn.setAttribute('aria-expanded', 'false');
    bar.appendChild(btn);
    btn.addEventListener('click', () => {
      openVisibilityPopover_(btn!);
    });
    btn.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        openVisibilityPopover_(btn!);
      }
    });
  }
  // Update label to reflect hidden-column count.
  const hiddenCount = state.getHiddenColumns().size;
  btn.textContent = hiddenCount > 0 ? `Columns ▾ (${hiddenCount} hidden)` : 'Columns ▾';
  btn.setAttribute('aria-pressed', hiddenCount > 0 ? 'true' : 'false');

  // Re-sync checkboxes if the popover is open (in case a filter popover commit
  // changed visible state while the popover was open — rare but possible).
  if (openVisibilityPopover) {
    const currentHidden = state.getHiddenColumns();
    for (const [key, cb] of openVisibilityPopover.checkboxes) {
      cb.checked = !currentHidden.has(key);
    }
  }
}
