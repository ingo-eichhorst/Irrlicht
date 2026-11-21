// Status bar text. Pure: no vscode import. Segments derive from STATES.
import type { ConnectionState } from './daemonClient';
import { STATES, type StateName } from './states';


const ICON: Record<StateName, string> = {
  working: '$(sync~spin)',
  waiting: '$(bell)',
  ready: '$(check)',
  error: '$(error)',
};

export function formatStatus(counts: Record<StateName, number>, conn: ConnectionState, daemonUrl: string): { text: string; tooltip: string } {
  if (conn === 'offline') {
    return {
      text: '$(debug-disconnect) Irrlicht: offline',
      tooltip: `The Irrlicht daemon is not reachable at ${daemonUrl}. It retries automatically.`,
    };
  }
  if (conn === 'connecting') {
    return { text: '$(sync~spin) Irrlicht', tooltip: `Connecting to the Irrlicht daemon at ${daemonUrl}` };
  }
  const text = STATES
    .filter((s) => s !== 'error' || counts[s] > 0)
    .map((s) => `${ICON[s]} ${counts[s]}`)
    .join('  ');
  const tooltip = [...STATES.map((s) => `${counts[s]} ${s}`), `Daemon: ${daemonUrl}`].join('\n');
  return { text, tooltip };
}
