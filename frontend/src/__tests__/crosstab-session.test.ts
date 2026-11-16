/**
 * Cross-tab logout sync (issues #493, #34). The session is an httpOnly
 * cookie, so a sign-out in one tab reaches the others over a
 * BroadcastChannel (or a storage-event marker where that is unavailable).
 */

jest.mock('../utils', () => ({
  ...jest.requireActual('../utils'),
  reloadPage: jest.fn(),
}));

type Channel = { name: string; onmessage: ((e: unknown) => void) | null; postMessage: jest.Mock; close: jest.Mock };
let channels: Channel[] = [];
const original = (globalThis as { BroadcastChannel?: unknown }).BroadcastChannel;

function firstChannel(): Channel {
  const c = channels[0];
  if (!c) throw new Error('no BroadcastChannel was opened');
  return c;
}

function installChannel(): void {
  (globalThis as { BroadcastChannel?: unknown }).BroadcastChannel = jest.fn((name: string) => {
    const c: Channel = { name, onmessage: null, postMessage: jest.fn(), close: jest.fn() };
    channels.push(c);
    return c;
  });
}

beforeEach(() => {
  channels = [];
  installChannel();
});

afterEach(() => {
  (globalThis as { BroadcastChannel?: unknown }).BroadcastChannel = original;
  jest.restoreAllMocks();
});

function loadAuth(): { api: typeof import('../api'); reload: jest.Mock } {
  let out: ReturnType<typeof loadAuth> | undefined;
  jest.isolateModules(() => {
    // eslint-disable-next-line @typescript-eslint/no-require-imports
    const { reloadPage: reload } = require('../utils') as { reloadPage: jest.Mock };
    // eslint-disable-next-line @typescript-eslint/no-require-imports
    const api = require('../api') as typeof import('../api');
    api.initAuth();
    out = { api, reload };
  });
  if (!out) throw new Error('loadAuth failed');
  return out;
}

describe('cross-tab logout sync (BroadcastChannel)', () => {
  test('a logout message from another tab clears state and reloads', () => {
    const { api, reload } = loadAuth();
    api.setAuthenticated(true);
    api.setCsrfToken('csrf');
    firstChannel().onmessage?.({ data: 'logout' });
    expect(api.isAuthenticated()).toBe(false);
    expect(api.getAuthHeaders()['X-CSRF-Token']).toBeUndefined();
    expect(reload).toHaveBeenCalledTimes(1);
  });

  test('other message payloads are ignored', () => {
    const { api, reload } = loadAuth();
    api.setAuthenticated(true);
    firstChannel().onmessage?.({ data: 'something-else' });
    expect(api.isAuthenticated()).toBe(true);
    expect(reload).not.toHaveBeenCalled();
  });

  test('a signed-out tab ignores the message and does not reload', () => {
    const { reload } = loadAuth();
    firstChannel().onmessage?.({ data: 'logout' });
    expect(reload).not.toHaveBeenCalled();
  });

  test('initAuth() is idempotent: one channel is opened', () => {
    const { api } = loadAuth();
    api.initAuth(); api.initAuth();
    expect(channels).toHaveLength(1);
    expect(firstChannel().name).toBe('cudly-auth');
  });

  test('broadcastLogout posts on the listening channel', () => {
    const { api } = loadAuth();
    api.broadcastLogout();
    expect(firstChannel().postMessage).toHaveBeenCalledWith('logout');
  });

  test('broadcastLogout before initAuth opens a short-lived channel', () => {
    let api!: typeof import('../api');
    jest.isolateModules(() => {
      // eslint-disable-next-line @typescript-eslint/no-require-imports
      api = require('../api') as typeof import('../api');
    });
    api.broadcastLogout();
    expect(firstChannel().postMessage).toHaveBeenCalledWith('logout');
    expect(firstChannel().close).toHaveBeenCalled();
  });
});

describe('cross-tab logout sync without BroadcastChannel', () => {
  beforeEach(() => {
    (globalThis as { BroadcastChannel?: unknown }).BroadcastChannel = undefined;
  });

  const storageEvt = (key: string | null): StorageEvent => new StorageEvent('storage', { key });

  test('the marker key from another tab signs this tab out', () => {
    const addSpy = jest.spyOn(window, 'addEventListener');
    const { api, reload } = loadAuth();
    const handler = addSpy.mock.calls.find(c => c[0] === 'storage')?.[1] as (e: StorageEvent) => void;
    api.setAuthenticated(true);
    handler(storageEvt('cudly:logout-signal'));
    expect(api.isAuthenticated()).toBe(false);
    expect(reload).toHaveBeenCalledTimes(1);
  });

  test('unrelated keys and localStorage.clear() do not sign the tab out', () => {
    const addSpy = jest.spyOn(window, 'addEventListener');
    const { api, reload } = loadAuth();
    const handler = addSpy.mock.calls.find(c => c[0] === 'storage')?.[1] as (e: StorageEvent) => void;
    api.setAuthenticated(true);
    handler(storageEvt('someOtherKey'));
    handler(storageEvt(null));
    expect(api.isAuthenticated()).toBe(true);
    expect(reload).not.toHaveBeenCalled();
  });

  test('broadcastLogout writes the non-secret marker', () => {
    const { api } = loadAuth();
    api.broadcastLogout();
    expect(localStorage.setItem).toHaveBeenCalledWith('cudly:logout-signal', expect.any(String));
  });

  test('broadcastLogout survives blocked storage', () => {
    const { api } = loadAuth();
    (localStorage.setItem as jest.Mock).mockImplementationOnce(() => { throw new Error('denied'); });
    expect(() => api.broadcastLogout()).not.toThrow();
  });
});
