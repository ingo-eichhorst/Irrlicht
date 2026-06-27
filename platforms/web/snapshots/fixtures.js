// Scenes for the web dashboard HTML snapshot artifacts (issue #757). Each scene
// is an initial /api/v1/sessions payload (the wrapped {groups:[...]} shape) plus
// optional websocket deltas (session_update / session_deleted messages, the same
// envelopes irrlicht.js consumes). render-html.test.js drives each scene through
// the real app in jsdom and serializes #session-list to a self-contained HTML.
//
// The render test pins the wall clock to FIXED_NOW_SEC, so first_seen offsets
// below produce deterministic elapsed labels (active sessions render
// Date.now()-first_seen).

export const FIXED_NOW_SEC = 1764800120
const t = (agoSec) => FIXED_NOW_SEC - agoSec

const group = (name, agents, costs = {}) => ({ name, agents, costs })
const sessions = (...groups) => ({ groups, provider_costs: {} })

export const scenes = [
  {
    name: '01-mixed-states',
    theme: 'dark',
    expectedRows: 3,
    sessions: sessions(
      group('irrlicht', [
        {
          session_id: 'proc-1', state: 'working', project_name: 'irrlicht',
          git_branch: 'main', adapter: 'claude-code', first_seen: t(125),
          metrics: {
            model_name: 'claude-opus-4-8', estimated_cost_usd: 0.42,
            context_utilization_percentage: 45, pressure_level: 'low',
            total_tokens: 48000, elapsed_seconds: 125,
          },
        },
        {
          session_id: 'proc-2', state: 'waiting', project_name: 'irrlicht',
          git_branch: 'feat/x', adapter: 'claude-code', first_seen: t(60),
          metrics: {
            model_name: 'claude-sonnet-4-6', context_utilization_percentage: 20,
            pressure_level: 'low', total_tokens: 12000,
          },
        },
        {
          session_id: 'proc-3', state: 'ready', project_name: 'irrlicht',
          git_branch: 'main', adapter: 'claude-code', first_seen: t(900),
          metrics: {
            model_name: 'claude-opus-4-8', estimated_cost_usd: 1.10,
            elapsed_seconds: 900,
          },
        },
      ], { day: 1.52 }),
    ),
    deltas: [],
  },

  {
    name: '02-antigravity-ghost',
    theme: 'dark',
    // One real session, then a transient antigravity PID=0 ghost arrives over
    // the wire beside it — the row Phase 1's daemon trace explains.
    expectedRows: 2,
    sessions: sessions(
      group('app', [
        {
          session_id: 'sess-real', state: 'working', project_name: 'app',
          git_branch: 'main', adapter: 'claude-code', first_seen: t(200),
          metrics: {
            model_name: 'claude-opus-4-8', estimated_cost_usd: 0.18,
            context_utilization_percentage: 30, pressure_level: 'low',
            total_tokens: 21000, elapsed_seconds: 200,
          },
        },
      ]),
    ),
    deltas: [
      {
        type: 'session_update',
        session: {
          session_id: 'proc-0', state: 'ready', project_name: 'app',
          git_branch: 'main', adapter: 'antigravity', first_seen: t(33),
          metrics: { model_name: 'gemini-3-pro', elapsed_seconds: 33 },
        },
      },
    ],
  },

  {
    name: '03-context-pressure',
    theme: 'dark',
    expectedRows: 2,
    sessions: sessions(
      group('irrlicht', [
        {
          session_id: 'proc-hot', state: 'working', project_name: 'irrlicht',
          git_branch: 'main', adapter: 'claude-code', first_seen: t(1800),
          metrics: {
            model_name: 'claude-opus-4-8', estimated_cost_usd: 4.20,
            context_utilization_percentage: 92, pressure_level: 'critical',
            total_tokens: 184000, elapsed_seconds: 1800,
          },
        },
        {
          session_id: 'proc-warm', state: 'working', project_name: 'irrlicht',
          git_branch: 'feat/y', adapter: 'claude-code', first_seen: t(300),
          metrics: {
            model_name: 'claude-sonnet-4-6', estimated_cost_usd: 0.65,
            context_utilization_percentage: 70, pressure_level: 'warning',
            total_tokens: 140000, elapsed_seconds: 300,
          },
        },
      ], { day: 4.85 }),
    ),
    deltas: [],
  },

  {
    name: '04-background-agents',
    theme: 'dark',
    // Mirrors irrlicht.bgbadge.test.js: a detached bg agent, an attached one,
    // and an ordinary session (no badge).
    expectedRows: 3,
    sessions: sessions(
      group('irrlicht', [
        {
          session_id: 'bg-detached', state: 'ready', project_name: 'irrlicht',
          adapter: 'claude-code', first_seen: t(600),
          metrics: { model_name: 'claude-opus-4-8', elapsed_seconds: 600 },
          background: { name: 'Add guiding colors to quest cards', detached: true },
        },
        {
          session_id: 'bg-attached', state: 'working', project_name: 'irrlicht',
          adapter: 'claude-code', first_seen: t(90),
          metrics: { model_name: 'claude-opus-4-8', total_tokens: 8000, elapsed_seconds: 90 },
          background: { name: 'Tidy imports' },
        },
        {
          session_id: 'normal', state: 'ready', project_name: 'irrlicht',
          adapter: 'claude-code', first_seen: t(1200),
          metrics: { model_name: 'claude-sonnet-4-6', elapsed_seconds: 1200 },
        },
      ]),
    ),
    deltas: [],
  },

  {
    name: '05-multi-group',
    theme: 'dark',
    // Two projects → group headers render.
    expectedRows: 3,
    sessions: sessions(
      group('irrlicht', [
        {
          session_id: 'irr-1', state: 'working', project_name: 'irrlicht',
          git_branch: 'main', adapter: 'claude-code', first_seen: t(140),
          metrics: {
            model_name: 'claude-opus-4-8', estimated_cost_usd: 0.31,
            context_utilization_percentage: 38, pressure_level: 'low',
            total_tokens: 30000, elapsed_seconds: 140,
          },
        },
        {
          session_id: 'irr-2', state: 'ready', project_name: 'irrlicht',
          git_branch: 'main', adapter: 'claude-code', first_seen: t(700),
          metrics: { model_name: 'claude-opus-4-8', elapsed_seconds: 700 },
        },
      ], { day: 0.9 }),
      group('webapp', [
        {
          session_id: 'web-1', state: 'waiting', project_name: 'webapp',
          git_branch: 'feat/login', adapter: 'codex', first_seen: t(45),
          metrics: {
            model_name: 'gpt-5-codex', context_utilization_percentage: 15,
            pressure_level: 'low', total_tokens: 9000,
          },
        },
      ], { day: 0.2 }),
    ),
    deltas: [],
  },

  {
    name: '06-light-theme',
    theme: 'light',
    expectedRows: 2,
    sessions: sessions(
      group('irrlicht', [
        {
          session_id: 'lt-1', state: 'working', project_name: 'irrlicht',
          git_branch: 'main', adapter: 'claude-code', first_seen: t(125),
          metrics: {
            model_name: 'claude-opus-4-8', estimated_cost_usd: 0.42,
            context_utilization_percentage: 45, pressure_level: 'low',
            total_tokens: 48000, elapsed_seconds: 125,
          },
        },
        {
          session_id: 'lt-2', state: 'ready', project_name: 'irrlicht',
          git_branch: 'main', adapter: 'claude-code', first_seen: t(900),
          metrics: { model_name: 'claude-opus-4-8', estimated_cost_usd: 1.10, elapsed_seconds: 900 },
        },
      ], { day: 1.52 }),
    ),
    deltas: [],
  },
]
