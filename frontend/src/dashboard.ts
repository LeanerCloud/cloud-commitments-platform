/**
 * Dashboard module for CUDly
 */

// Re-export for backward compatibility (tests and other callers import from dashboard.ts).
export { formatTrendAxisTick } from './modules/chart-utils';

export { savingsLayerColors, buildFilterDesc } from './dashboardflow/chartShared';
export { computeServiceStats, MIXED_VARIANT_LABEL, computeServiceStatsFromRecs } from './dashboardflow/serviceStats';
export type { ServiceSavingsStats } from './dashboardflow/serviceStats';
export { __test__ } from './dashboardflow/summary';
export { renderSavingsByService } from './dashboardflow/byService';
export { loadSavingsTrendChart, loadYtdSparkline, setupSavingsTrendHandlers } from './dashboardflow/trend';
export { canCancelUpcomingPurchase } from './dashboardflow/upcoming';
export { setupDashboardHandlers, loadDashboard } from './dashboardflow/load';
