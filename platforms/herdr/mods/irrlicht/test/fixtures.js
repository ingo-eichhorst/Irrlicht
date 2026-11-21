// Fixtures taken from a live run on 2026-11-21: Herdr 0.1.16 with herdr-core
// 0.1.21, Claude Code 2.1.330 and Codex 0.163.0 each started in a Herdr tab,
// and the Irrlicht daemon's GET /api/v1/sessions read at the same moment.
// Trimmed to the fields the mod reads plus a few neighbours, with the user's
// home directory replaced by /Users/dev, and the run's /tmp/herdr-demo (typed)
// and /private/tmp/herdr-demo (resolved) moved to /var and /private/var: the
// same macOS symlink shape, without naming a publicly writable directory
// (SonarCloud javascript:S5443). The pids are the measured ones: they
// carry the finding that Codex's session pid (46690) is the native binary
// below the `node` launcher Herdr reports as the foreground (46686).

export const LIVE_AGENTS = [
  {
    address: 't3', pane: 'p35e207e9', space: 'spa2c7bb0', space_name: 'herdr-demo',
    tab: 'tb55c6b36', tab_index: 1, tab_name: '1', status: 'done', app: 'claude-code',
    program: { key: 'claude', name: 'Claude Code', icon: 'icons/claude.svg', tint: false, agent: true },
    title: 'TCP slow start notes', seq: 11, pinned: false, focused: false,
  },
  {
    address: 't4', pane: 'p6de9a2fe', space: 'spa2c7bb0', space_name: 'herdr-demo',
    tab: 'tbd9ae64d', tab_index: 2, tab_name: '2', status: 'done', app: 'codex',
    program: { key: 'codex', name: 'Codex', icon: 'icons/codex.svg', tint: false, agent: true },
    title: 'Create DNS resolution notes | codex', seq: 12, pinned: false, focused: false,
  },
];

export const LIVE_TERMINALS = [
  {
    address: 't1', pid: 21596, cwd: '/Users/dev', state: 'live', status: null, report: null,
    foreground: { argv: ['/bin/zsh', '-l'], cwd: '/Users/dev', name: 'zsh', pid: 21596 },
  },
  {
    address: 't3', pid: 45877, cwd: '/var/herdr-demo/claude', state: 'live', status: 'done',
    report: { app: 'claude-code', state: 'done' },
    foreground: { argv: ['claude'], cwd: '/private/var/herdr-demo/claude', name: '2.1.330', pid: 46222 },
  },
  {
    address: 't4', pid: 46387, cwd: '/var/herdr-demo/codex', state: 'live', status: 'idle',
    report: { app: 'codex', state: 'idle' },
    foreground: {
      argv: ['node', '/Users/dev/.nvm/versions/node/v22.18.0/bin/codex', '--no-daemon'],
      cwd: '/private/var/herdr-demo/codex', name: 'node', pid: 46686,
    },
  },
];

export const CLAUDE_SESSION = {
  version: 1,
  session_id: 'd042c4a9-aeed-43fe-929b-012be3d17727',
  state: 'waiting',
  adapter: 'claude-code',
  cwd: '/private/var/herdr-demo/claude',
  project_name: 'claude',
  pid: 46222,
  launcher: { term_program: 'ghostty', tty: '/dev/ttys025' },
  metrics: {
    total_tokens: 46400,
    model_name: 'claude-opus-5-5',
    context_window: 1000000,
    context_utilization_percentage: 4.64,
    pressure_level: 'safe',
  },
};

export const CODEX_SESSION = {
  version: 1,
  session_id: '01a1fcba-f6c8-7840-9448-b12d3b6e51cc',
  state: 'ready',
  adapter: 'codex',
  cwd: '/private/var/herdr-demo/codex',
  project_name: 'codex',
  pid: 46690,
  launcher: { term_program: 'ghostty', tty: '/dev/ttys029' },
  metrics: {
    total_tokens: 18758,
    model_name: 'gpt-6.1-sol',
    context_window: 475000,
    context_utilization_percentage: 3.9490526315789474,
    pressure_level: 'safe',
  },
};

// A session outside Herdr (another terminal app), and a subagent child as the
// live payload carried it: no pid of its own. Synthetic ids and folder, in the
// shape of a session read on the same run.
export const OTHER_SESSION = {
  version: 1,
  session_id: '00000000-0000-4000-8000-000000000001',
  state: 'ready',
  adapter: 'claude-code',
  cwd: '/Users/dev/other-repo',
  pid: 70001,
  launcher: { term_program: 'zed', tty: '/dev/ttys040' },
  metrics: {
    total_tokens: 199022,
    model_name: 'claude-opus-5-5',
    context_window: 1000000,
    context_utilization_percentage: 19.9022,
    pressure_level: 'safe',
  },
  children: [
    {
      version: 1,
      session_id: 'agent-a1b2c3',
      parent_session_id: '00000000-0000-4000-8000-000000000001',
      state: 'working',
      adapter: 'claude-code',
      cwd: '/Users/dev/other-repo',
      metrics: { total_tokens: 900, context_utilization_percentage: 0.09, pressure_level: 'safe' },
    },
  ],
};

export function livePayload(...extraGroups) {
  return {
    groups: [
      { name: 'codex', agents: [CODEX_SESSION] },
      { name: 'claude', agents: [CLAUDE_SESSION] },
      { name: 'other-repo', agents: [OTHER_SESSION] },
      ...extraGroups,
    ],
    provider_costs: {},
  };
}

// The address ladder's io (resolveDaemonAddress), faked: environment values
// and files by path. Every file check and read is recorded, so "the file was
// never read" is an observation rather than an absence.
export const HOME_ADDR = '/home/dev/.local/share/irrlicht/irrlichd.addr';

export function world({ env = {}, files = {} } = {}) {
  const asked = [];
  return {
    asked,
    io: {
      bindAddr: async () => env.IRRLICHT_BIND_ADDR,
      irrlichtHome: async () => env.IRRLICHT_HOME,
      home: async () => env.HOME,
      isRegularFile: async (path) => {
        asked.push(['isRegularFile', path]);
        const f = files[path];
        return !!f && !f.isLink && (f.kind || 'file') === 'file';
      },
      read: async (path) => {
        asked.push(['read', path]);
        const f = files[path];
        return f ? f.text : null;
      },
    },
  };
}
