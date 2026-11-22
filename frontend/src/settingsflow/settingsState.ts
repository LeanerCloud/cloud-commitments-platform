import type * as api from '../api';

/** Mutable settings state shared by the load and save flows. */
export const settingsState: {
  cachedSourceCloud: string | undefined;
  // Cached service configs from last load — used as base when saving to preserve
  // non-UI fields when a row is re-keyed. For an existing row the server overlays only
  // the UI-editable keys and ignores ramp_schedule; it honours ramp_schedule only on a row's first save.
  loadedServiceConfigs: api.ServiceConfig[];
  // Rows loaded from an old Settings id (#613) and not yet written under the
  // current one; keyed `provider/service`.
  legacyRekeyedServices: Set<string>;
} = {
  cachedSourceCloud: undefined,
  loadedServiceConfigs: [],
  legacyRekeyedServices: new Set<string>(),
};
