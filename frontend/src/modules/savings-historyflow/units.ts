/**
 * Savings unit and series selection helpers for the Savings History view.
 */

import { HOURS_PER_MONTH } from '../../lib/hours-per-month';

// Canonical unit the API returns for per-bucket and summary values.
// estimated_savings in purchase_history is the monthly savings figure
// (it sits alongside monthly_cost in the schema), so all totals summed
// from that column are also monthly.
export const API_UNIT = 'monthly' as const;

export type SavingsUnit = 'hourly' | 'monthly' | 'yearly';

// Conversion factors relative to monthly as the canonical unit. Hourly uses
// the shared 730-hour month (365 * 24 / 12), the same as the backend.
const MONTHS_PER_YEAR = 12;

/**
 * Convert a monthly savings value to the chosen display unit.
 * The API always returns monthly values; this function converts for display only.
 */
export function convertFromMonthly(monthlyValue: number, unit: SavingsUnit): number {
    switch (unit) {
        case 'hourly':  return monthlyValue / HOURS_PER_MONTH;
        case 'monthly': return monthlyValue;
        case 'yearly':  return monthlyValue * MONTHS_PER_YEAR;
    }
}

/**
 * Return the short suffix string for the given unit (e.g. "/hr").
 */
export function unitSuffix(unit: SavingsUnit): string {
    switch (unit) {
        case 'hourly':  return '/hr';
        case 'monthly': return '/mo';
        case 'yearly':  return '/yr';
    }
}

/**
 * Return the adjective for use in stat-card headings (e.g. "Hourly").
 */
export function unitLabel(unit: SavingsUnit): string {
    switch (unit) {
        case 'hourly':  return 'Hourly';
        case 'monthly': return 'Monthly';
        case 'yearly':  return 'Yearly';
    }
}

/**
 * Read the current value of the #savings-unit dropdown.
 * Falls back to the API's canonical unit when the element is absent (e.g. in tests
 * that don't include the dropdown in their DOM fixture).
 */
export function getSelectedUnit(): SavingsUnit {
    const el = document.getElementById('savings-unit') as HTMLSelectElement | null;
    const val = el?.value ?? API_UNIT;
    if (val === 'hourly' || val === 'monthly' || val === 'yearly') return val;
    return API_UNIT;
}

/**
 * Which series the chart plots (issue #749). Period savings and the running
 * total have different scales and units, so they are shown one at a time on
 * a single axis instead of on two axes whose line crossings mean nothing.
 */
export type SavingsSeries = 'period' | 'cumulative';

export function getSelectedSeries(): SavingsSeries {
    const el = document.getElementById('savings-series') as HTMLSelectElement | null;
    return el?.value === 'cumulative' ? 'cumulative' : 'period';
}
