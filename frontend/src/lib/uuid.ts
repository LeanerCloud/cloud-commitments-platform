// crypto.randomUUID exists only in secure contexts, and the app also runs over plain HTTP.
export function newUuid(): string {
  const source = globalThis.crypto;
  if (typeof source?.randomUUID === 'function') return source.randomUUID().toLowerCase();
  if (typeof source?.getRandomValues !== 'function') throw new Error('Secure random numbers are unavailable in this browser.');
  const bytes = source.getRandomValues(new Uint8Array(16));
  bytes[6] = bytes[6]! & 0x0f | 0x40; bytes[8] = bytes[8]! & 0x3f | 0x80;
  const hex = Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
