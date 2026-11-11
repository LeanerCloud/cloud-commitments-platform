/**
 * Helpers for the upcoming-purchases widgets (issue #713). The Home widget
 * is scoped server-side by the chips; these cover row matching and the
 * filter-aware empty text.
 */

function isProviderFilter(provider: string): boolean {
  return provider !== '' && provider.toLowerCase() !== 'all';
}

/**
 * A bound plan whose groups span providers has provider "multiple" and lists
 * its group providers in `providers`; a row matches a chip on either.
 */
export function rowMatchesProvider(
  row: { provider?: string; providers?: readonly string[] },
  provider: string,
): boolean {
  const want = provider.toLowerCase();
  return (row.provider || '').toLowerCase() === want
    || (row.providers ?? []).some(p => p.toLowerCase() === want);
}

/** Empty-state text, naming the provider filter when one hid every row. */
export function upcomingEmptyMessage(provider: string): string {
  return isProviderFilter(provider)
    ? `No upcoming scheduled purchases for ${provider.toUpperCase()}`
    : 'No upcoming scheduled purchases';
}
