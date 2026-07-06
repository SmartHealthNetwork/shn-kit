// bridge.ts — the Electron preload bridge (window.kit) + browser-mode fallbacks.
// The renderer must work identically whether launched inside Electron
// (window.kit present, token comes from the preload session) or in a plain
// browser during development (token comes from the ?token= query string).

export interface KitBridge {
  getSession(): Promise<{ api: string; token: string }>;
  restart(): Promise<void>;
  openExternal(url: string): Promise<void>;
}

declare global {
  interface Window {
    kit?: KitBridge;
  }
}

const TOKEN_STORAGE_KEY = 'kitToken';

// Resolved once per page load — the token doesn't change out from under us
// mid-session, so later calls just return the cached value. We cache the
// PROMISE (not the resolved value): two concurrent resolveToken() callers
// must share one in-flight window.kit.getSession() call rather than each
// triggering their own (the S5 smoke-found race). A rejection clears the
// cache so a subsequent retry re-invokes doResolve() instead of replaying
// the same failure forever.
let cached: Promise<string> | undefined;

async function doResolve(): Promise<string> {
  if (window.kit) {
    const session = await window.kit.getSession();
    return session.token;
  }

  const url = new URL(window.location.href);
  const queryToken = url.searchParams.get('token');
  if (queryToken) {
    sessionStorage.setItem(TOKEN_STORAGE_KEY, queryToken);
    url.searchParams.delete('token');
    window.history.replaceState({}, '', url.toString());
    return queryToken;
  }

  return sessionStorage.getItem(TOKEN_STORAGE_KEY) ?? '';
}

export function resolveToken(): Promise<string> {
  if (cached) return cached;

  cached = doResolve().catch((err: unknown) => {
    cached = undefined;
    throw err;
  });
  return cached;
}

export function canRestart(): boolean {
  return window.kit !== undefined;
}

export function restartKit(): Promise<void> {
  if (window.kit) return window.kit.restart();
  return Promise.reject(new Error('restart is unavailable outside the desktop app'));
}

export function openExternal(url: string): void {
  if (window.kit) {
    void window.kit.openExternal(url);
    return;
  }
  window.open(url, '_blank');
}
