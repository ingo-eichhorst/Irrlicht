// Resolves the irrlicht.daemonUrl setting. Pure: no vscode import.
export const DEFAULT_DAEMON_URL = 'http://127.0.0.1:7837';
export interface ResolvedDaemonUrl { origin: string; wsUrl: string; warning?: string }

const STREAM_PATH = '/api/v1/sessions/stream';
const HTTP_PROTOCOLS = new Set(['http:', 'https:']);

function isLoopbackHost(hostname: string): boolean {
  if (hostname === 'localhost' || hostname === '[::1]') return true;
  const m = /^127\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(hostname);
  return m !== null && m.slice(1).every((o) => Number(o) <= 255);
}

function build(origin: string, warning?: string): ResolvedDaemonUrl {
  const wsUrl = origin.replace(/^http/, 'ws') + STREAM_PATH;
  return { origin, wsUrl, warning };
}

export function resolveDaemonUrl(raw: unknown): ResolvedDaemonUrl {
  const reject = (): ResolvedDaemonUrl =>
    build(
      DEFAULT_DAEMON_URL,
      `Irrlicht: ignoring irrlicht.daemonUrl ${JSON.stringify(raw)} (only http(s) loopback URLs are allowed); using ${DEFAULT_DAEMON_URL}.`,
    );
  if (typeof raw !== 'string' || raw.trim() === '') return reject();
  let u: URL;
  try {
    u = new URL(raw.trim());
  } catch {
    return reject();
  }
  if (!HTTP_PROTOCOLS.has(u.protocol) || !isLoopbackHost(u.hostname)) return reject();
  return build(u.origin);
}

/** The resolved daemon URL, read once and re-resolved only when the setting changes. */
export interface DaemonUrlSource {
  current(): ResolvedDaemonUrl;
  /** Fires only when the origin changed. */
  onDidChangeOrigin(l: (u: ResolvedDaemonUrl) => void): { dispose(): void };
}
