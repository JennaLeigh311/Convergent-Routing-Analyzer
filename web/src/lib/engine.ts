// Engine endpoint resolution.
//
// The app NEVER hardcodes a dev-only localhost. It speaks to its own origin under
// stable paths and lets the serving layer route to the engine:
//
//   - In production (the nginx image), nginx reverse-proxies `/api/*` -> engine
//     REST and `/stream` -> engine WebSocket (see web/nginx.conf), so the browser
//     only ever talks to the page's own origin.
//   - In `npm run dev`, the Vite dev server proxies the same paths (vite.config.ts).
//
// A deployment can still override the engine origin at RUNTIME (without a rebuild)
// by setting `window.__ENGINE_BASE__` from a small served config script, or at
// BUILD time via `VITE_ENGINE_BASE`. When neither is set we default to the current
// origin (the proxy path above), which is the compose/default case.

declare global {
  interface Window {
    __ENGINE_BASE__?: string;
  }
}

/** Trim a trailing slash so we can append paths uniformly. */
function trimSlash(s: string): string {
  return s.endsWith("/") ? s.slice(0, -1) : s;
}

/**
 * The HTTP base for REST calls. Resolution order: runtime override
 * (`window.__ENGINE_BASE__`) -> build-time `VITE_ENGINE_BASE` -> the page origin
 * with the `/api` proxy prefix.
 */
export function httpBase(): string {
  const runtime = typeof window !== "undefined" ? window.__ENGINE_BASE__ : undefined;
  if (runtime) return trimSlash(runtime);

  const built = import.meta.env?.VITE_ENGINE_BASE as string | undefined;
  if (built) return trimSlash(built);

  // Same-origin proxy prefix (nginx / vite dev both map /api -> engine root).
  return `${trimSlash(window.location.origin)}/api`;
}

/** Build a REST URL for a path like `/graph`. */
export function restUrl(path: string): string {
  return `${httpBase()}${path.startsWith("/") ? path : `/${path}`}`;
}

/**
 * The WebSocket URL for `/stream`, derived from the same base. We switch the http
 * scheme to ws and DROP the `/api` proxy prefix because the stream proxy is mounted
 * at `/stream` directly (both nginx and the vite dev proxy). When an explicit
 * engine base is configured we honor its scheme/host and append `/stream` there.
 */
export function streamUrl(query: string): string {
  const base = httpBase();
  let wsBase: string;
  if (base.endsWith("/api")) {
    // Same-origin proxy: /stream lives at the origin root, not under /api.
    const origin = base.slice(0, -"/api".length);
    wsBase = origin.replace(/^http/, "ws");
  } else {
    wsBase = base.replace(/^http/, "ws");
  }
  const qs = query ? `?${query}` : "";
  return `${wsBase}/stream${qs}`;
}
