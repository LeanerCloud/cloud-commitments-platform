import type { ConvertibleRI, RIUtilization, ReshapeRecommendation } from '../api';

// Mutable state shared by the RI Exchange modules. One object, never re-created;
// modules read and write its fields directly (no destructured snapshots).
export const riexState: {
  currentRIs: ConvertibleRI[];
  currentRIsLoaded: boolean;
  currentUtilization: Map<string, RIUtilization>;
  utilizationStatus: 'loading' | 'loaded' | 'failed';
  currentRecommendations: ReshapeRecommendation[];
  currentRecsLoaded: boolean;
  riAccountNames: Map<string, string>;
  utilizationGeneration: number;
  scopeGeneration: number;
  currentRIAccountID: string | undefined;
} = {
  currentRIs: [],
  // True once currentRIs holds the convertible-RI list of the scope being
  // shown. The reshape empty state only quotes currentRIs.length while this is
  // set, so it never prints the previous scope's count, or a count from a list
  // that failed to load (issue #1135).
  currentRIsLoaded: false,
  currentUtilization: new Map(),
  // An RI missing from currentUtilization is still loading only while this is
  // 'loading'; after that its utilization is unknown (issue #1687).
  utilizationStatus: 'loading',
  currentRecommendations: [],
  // True once currentRecommendations holds this scope's successfully loaded
  // reshape list, so a late utilization result never overwrites a load error.
  currentRecsLoaded: false,
  // Registered account names by AWS account ID, for the account label (#1692).
  riAccountNames: new Map(),
  // Generation counter to prevent stale utilization data from overwriting fresh data
  utilizationGeneration: 0,
  // Generation counter so a superseded loadRIExchange (chip changed while the
  // account lookup was in flight) never issues its list call.
  scopeGeneration: 0,
  // The AWS account the convertible-RI list is currently scoped to (the
  // single-select account chip value, or undefined for all-accounts). Tracked
  // at module scope so the asynchronous utilization re-render preserves the
  // scoped empty-state copy instead of falling back to the unscoped message
  // (issue #871).
  currentRIAccountID: undefined,
};
