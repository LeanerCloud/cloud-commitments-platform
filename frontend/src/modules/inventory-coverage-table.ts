import type { ProviderCoverageSection, CoverageServiceRow } from '../api';
import {
  buildCoverageBar, coverageTargetFor, type CoverageTargets,
} from '../lib/coverage-bar';
import { formatCurrency } from '../utils';
import { ownValue } from '../lib/own-property';
import { appendCell, clearChildren, renderEmptyParagraph } from './inventory-dom';

const PROVIDER_DISPLAY_NAMES: Record<string, string> = {
  aws: 'AWS',
  azure: 'Azure',
  gcp: 'GCP',
};

/**
 * Build a context-aware empty-state message for a per-provider Coverage card.
 * When chip filters are active the message names the scope so the user knows
 * the result is filtered rather than genuinely absent usage.
 *
 * @param providerLabel - Display name of the provider (e.g. "AWS").
 * @param activeProvider - The provider chip value, if one is set.
 * @param activeAccountID - The account chip value, if exactly one is selected.
 */
function buildCoverageEmptyMessage(
  providerLabel: string,
  activeProvider?: string,
  activeAccountID?: string,
): string {
  if (activeProvider || activeAccountID) {
    return `No ${providerLabel} usage for the selected account/filter.`;
  }
  return `No usage detected for ${providerLabel}.`;
}

/**
 * Render coverage sections. Each provider gets its own card. Providers
 * with services=null show an empty-state paragraph. All text is set via
 * textContent -- no innerHTML -- so no escaping helper is needed (XSS posture
 * matches the active-commitments section per issue #340).
 */
export function renderCoverageBreakdown(
  container: HTMLElement,
  providers: ProviderCoverageSection[],
  targets: CoverageTargets,
  activeProvider?: string,
  activeAccountID?: string,
): void {
  clearChildren(container);

  if (!providers || providers.length === 0) {
    renderEmptyParagraph(container, 'No coverage data available.');
    return;
  }

  for (const section of providers) {
    container.appendChild(buildProviderSection(section, targets, activeProvider, activeAccountID));
  }
}

function buildProviderSection(
  section: ProviderCoverageSection,
  targets: CoverageTargets,
  activeProvider?: string,
  activeAccountID?: string,
): HTMLElement {
  const card = document.createElement('section');
  card.className = 'card coverage-provider-card';

  // Header row: provider name + overall coverage badge.
  const header = document.createElement('div');
  header.className = 'section-header';

  const providerLabel = ownValue(PROVIDER_DISPLAY_NAMES, section.provider) ?? section.provider.toUpperCase();

  const title = document.createElement('h3');
  title.textContent = providerLabel;
  header.appendChild(title);

  if (section.overall_coverage_pct !== null && section.overall_coverage_pct !== undefined) {
    const badge = document.createElement('span');
    badge.className = 'coverage-overall-badge';
    badge.textContent = `Overall: ${section.overall_coverage_pct.toFixed(1)}% covered`;
    header.appendChild(badge);
  }
  card.appendChild(header);

  // Body: empty-state or per-service table.
  if (!section.services || section.services.length === 0) {
    const empty = document.createElement('p');
    empty.className = 'empty';
    empty.textContent = buildCoverageEmptyMessage(providerLabel, activeProvider, activeAccountID);
    card.appendChild(empty);
    return card;
  }

  card.appendChild(buildServiceTable(section.services, (service) => coverageTargetFor(targets, section.provider, service)));
  return card;
}

function buildServiceTable(
  rows: CoverageServiceRow[],
  targetFor: (service: string) => number | null,
): HTMLTableElement {
  const table = document.createElement('table');
  table.className = 'coverage-service-table table-compact';

  const thead = document.createElement('thead');
  const headerRow = document.createElement('tr');
  for (const label of ['Service', 'Covered/mo', 'On-Demand Gap/mo', 'Coverage %', 'Coverage Bar']) {
    const th = document.createElement('th');
    th.textContent = label;
    if (label.endsWith('/mo')) th.className = 'num';
    if (label === 'Coverage Bar') {
      th.setAttribute('aria-label', 'Coverage bar');
    }
    headerRow.appendChild(th);
  }
  thead.appendChild(headerRow);
  table.appendChild(thead);

  const tbody = document.createElement('tbody');
  for (const row of rows) {
    tbody.appendChild(buildServiceRow(row, targetFor(row.service)));
  }
  table.appendChild(tbody);
  return table;
}

function buildServiceRow(row: CoverageServiceRow, target: number | null): HTMLTableRowElement {
  const tr = document.createElement('tr');

  appendCell(tr, row.service);
  appendCell(tr, formatCurrency(row.covered_monthly), true);
  appendCell(tr, formatCurrency(row.on_demand_monthly), true);
  appendCell(tr, row.coverage_pct !== null && row.coverage_pct !== undefined
    ? `${row.coverage_pct.toFixed(1)}%`
    : 'N/A');
  // Bar cell: visual coverage indicator.
  const barTd = document.createElement('td');
  barTd.className = 'coverage-bar-cell';
  if (row.coverage_pct !== null && row.coverage_pct !== undefined) {
    barTd.appendChild(buildCoverageBar(row.coverage_pct, target));
  } else {
    // No coverage figure: say so rather than leaving the cell blank, which
    // reads as a rendering failure. A 0%-width bar is not an option -- it
    // would claim we measured zero coverage.
    const absent = document.createElement('span');
    absent.className = 'coverage-bar-absent';
    absent.textContent = 'N/A';
    barTd.appendChild(absent);
  }
  tr.appendChild(barTd);

  return tr;
}
