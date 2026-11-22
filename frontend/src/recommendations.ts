/**
 * Recommendations module entry point for CUDly. The implementation lives in
 * ./recsflow/*; this file only re-exports the public API so existing
 * importers keep working (issue #210).
 */

// Re-export the shared primitives so existing consumers that import from
// recommendations.ts keep working without import-path churn (issue #166).
export { parseNumericFilter } from './lib/column-filters';
export type { ParsedNumericFilter } from './lib/column-filters';
export { getPurchaseModalSession, getPurchaseModalRecommendations, clearPurchaseModalRecommendations, resetExpandedCells } from './recsflow/recsState';
export { getAccountName } from './recsflow/shared';
export { effectiveMonthlySavings, effectiveSavingsPct, displaySavingsPct, weightedDisplaySavingsPct, onDemandMonthly, scaleCost, formatCostForPeriod, periodSuffix } from './recsflow/money';
export { cellSummary, pageLevelRange, formatSavingsRange } from './recsflow/ranges';
export type { CellRangeSummary, PageLevelRange } from './recsflow/ranges';
export { COLUMN_DEFS } from './recsflow/columnDefs';
export type { ColumnDef } from './recsflow/columnDefs';
export { TOGGLEABLE_COLUMNS, DEFAULT_HIDDEN_COLUMNS, loadColumnVisibility, saveColumnVisibility, loadColumnFilters, saveColumnFilters, resetColumnFiltersState, resetColumnVisibilityState } from './recsflow/columnPrefs';
export { applyColumnFilters, formatCapacity, displayPrecision } from './recsflow/cellValues';
export { groupRecsByCell, spGroupKey, groupSpCellKeys, pickBestVariantPerCell, scaleRecForCapacity } from './recsflow/groups';
export { fanOutBucketAccountLabel, getFanOutBuckets, clearFanOutBuckets, getExecuteMode, clearExecuteMode } from './recsflow/fanOutState';
export type { FanOutBucket } from './recsflow/fanOutState';
export { retainFanOutBuckets } from './recsflow/fanOutSummary';
export { openPurchaseModal } from './recsflow/purchaseModal';
export { renderUsageSparkline } from './recsflow/cells';
export { seedGlobalDefaults, resetCachedGlobalConfig, loadRecommendations } from './recsflow/load';
export { STALE_THRESHOLD_MS, resetAutoRefreshInFlight, describeSkippedRecommendations, describeExpiredPricingHolds, triggerAutoRefreshIfStale, startRecommendationsRefresh, refreshRecommendations } from './recsflow/refresh';
export { LOOKBACK_SCOPE_HINT } from './recsflow/lookback';
export { setupRecommendationsHandlers } from './recsflow/handlers';
