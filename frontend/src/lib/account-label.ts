/** "Name (id)", without repeating an id the name already contains. */
export function accountDisplayLabel(name: string, externalId: string): string {
  if (!name) return externalId;
  return name.includes(externalId) ? name : `${name} (${externalId})`;
}
