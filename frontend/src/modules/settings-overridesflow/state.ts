export type GlobalDefaults = { term: number; payment: string; coverage: number };

export type OverrideModalContext = {
  accountId: string;
  provider: string;
  panel: HTMLElement;
  /** Services available for bulk selection (excludes already-overridden). */
  availableServices: ReadonlyArray<{ value: string; label: string }>;
};

// Cached global defaults surfaced by loadGlobalSettings. Used by the override
// modal (issue #112) to show "Inherit (currently: X)" labels so the user can
// see what they'd be overriding without switching tabs.
// overrideModalContext: state carried from openOverrideModal → submitOverrideForm so the form
// handler knows which account/provider/panel it belongs to without
// inspecting the DOM. Cleared on close so it can't leak across opens.
export const overridesState: {
  cachedGlobalDefaults: GlobalDefaults;
  overrideModalContext: OverrideModalContext | null;
} = {
  cachedGlobalDefaults: { term: 3, payment: 'no-upfront', coverage: 80 },
  overrideModalContext: null,
};

/**
 * Override the cached global defaults used for the "Inherit (currently: X)"
 * labels. Test-only; production code updates the cache via loadGlobalSettings.
 */
export function setGlobalDefaultsForTest(defaults: GlobalDefaults): void {
  overridesState.cachedGlobalDefaults = { ...defaults };
}

export function setCachedGlobalDefaults(defaults: GlobalDefaults): void {
  overridesState.cachedGlobalDefaults = defaults;
}
