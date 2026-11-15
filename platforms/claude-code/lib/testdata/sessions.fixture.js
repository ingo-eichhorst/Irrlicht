// A synthetic /api/v1/sessions payload: made-up projects, paths, ids and
// account refs, shaped by the JSON tags of core/cmd/irrlichd/handlers.go's
// sessionsResponse and core/domain/session's AgentGroup, Agent, SessionState,
// SessionMetrics and RateLimitSnapshot. No field here was copied from a real
// daemon's answer.
//
// It is a .js module rather than .json so both test runners can import it:
// vitest (lib/*.test.js) and `claude plugin test` (tests/*.test.tsx), whose
// environment loads code files only.
//
// What it covers, by design:
//   - two project groups and an orchestrator group with a nested group;
//   - an agent with a child (a subagent row, drawn indented);
//   - an empty `adapter` (Claude Code, untagged) beside an explicit
//     "claude-code", and two non-Claude adapters ("pi", "codex");
//   - `metrics.model_name` set where `model` is not, and the reverse;
//   - one session waiting on a question (question_headline), one with only a
//     last reply (last_assistant_text);
//   - two confirmed accounts on one provider, the "here" session on the older
//     sample of the two, so "current bucket" and "newest bucket" differ;
//   - provider_costs with an `unattributed` key, which the 24h footer counts.

export const NOW_MS = 1_900_000_000_000; // a fixed "now" for every test
const NOW_S = NOW_MS / 1000;

export const HERE_ID = 'sess-here-0001';

export const SESSIONS_PAYLOAD = {
  groups: [
    {
      name: 'orchard',
      costs: { day: 4.5, week: 20, month: 40, year: 400 },
      agents: [
        {
          version: 3,
          session_id: HERE_ID,
          state: 'working',
          adapter: '',
          cwd: '/home/dev/src/orchard',
          git_branch: 'main',
          project_name: 'orchard',
          first_seen: NOW_S - 600,
          updated_at: NOW_S - 2,
          confidence: 'high',
          event_count: 42,
          last_event: 'PostToolUse',
          metrics: {
            elapsed_seconds: 605,
            total_tokens: 177_000,
            model_name: 'claude-opus-5-5',
            context_utilization_percentage: 17.7,
            estimated_cost_usd: 1.234,
            last_assistant_text: 'Refactored the\nloader into two passes.',
            rate_limit: {
              windows: [
                { used_percent: 11, window_minutes: 300, resets_at: NOW_S + 2 * 3600 + 14 * 60 },
                { used_percent: 83, window_minutes: 10080, resets_at: NOW_S + 3 * 86400 + 4 * 3600 },
              ],
              sampled_at: NOW_S - 120,
              provider: 'anthropic',
              product: 'claude_max',
              confirmed_account_ref: 'acct-test-0001',
              attribution_quality: 'confirmed',
            },
          },
          account_binding: { decided: true, ref: 'binding-test-aaaa', confirmed_at: NOW_S - 900 },
          children: [
            {
              version: 3,
              session_id: 'sess-child-0002',
              state: 'ready',
              adapter: 'claude-code',
              parent_session_id: HERE_ID,
              git_branch: 'main',
              first_seen: NOW_S - 300,
              updated_at: NOW_S - 30,
              confidence: 'high',
              event_count: 7,
              last_event: 'Stop',
              metrics: {
                elapsed_seconds: 45,
                total_tokens: 4_200,
                model_name: 'claude-haiku-4-5-20251001',
                context_utilization_percentage: 2.1,
              },
            },
          ],
        },
        {
          version: 3,
          session_id: 'sess-other-0003',
          state: 'waiting',
          adapter: 'claude-code',
          model: 'claude-sonnet-4-5',
          git_branch: 'feature/a-very-long-branch-name-that-will-not-fit',
          first_seen: NOW_S - 4000,
          updated_at: NOW_S - 10,
          confidence: 'high',
          event_count: 99,
          last_event: 'PermissionRequest',
          metrics: {
            elapsed_seconds: 3725,
            total_tokens: 66_000,
            context_utilization_percentage: 6.6,
            estimated_cost_usd: 0.004,
            question_headline: 'Apply the migration to the staging database?',
            last_assistant_text: 'I would like to run the migration.',
            rate_limit: {
              windows: [
                { used_percent: 40, window_minutes: 300, resets_at: NOW_S + 3600 },
              ],
              sampled_at: NOW_S - 5,
              provider: 'anthropic',
              plan_type: 'pro',
              confirmed_account_ref: 'acct-test-0002',
              attribution_quality: 'confirmed',
            },
          },
        },
      ],
    },
    {
      name: 'lighthouse',
      agents: [
        {
          version: 3,
          session_id: 'sess-pi-0004',
          state: 'ready',
          adapter: 'pi',
          git_branch: 'dev',
          first_seen: NOW_S - 7200,
          updated_at: NOW_S - 600,
          confidence: 'medium',
          event_count: 12,
          last_event: 'agent_end',
          metrics: {
            elapsed_seconds: 7000,
            total_tokens: 40_000,
            model_name: 'gpt-5.4',
            context_utilization_percentage: 4,
          },
        },
      ],
    },
    {
      name: 'fleet',
      type: 'gastown',
      agents: [],
      groups: [
        {
          name: 'rig-a',
          agents: [
            {
              version: 3,
              session_id: 'sess-codex-0005',
              state: 'error',
              adapter: 'codex',
              git_branch: 'fix/flaky',
              first_seen: NOW_S - 100,
              updated_at: NOW_S - 50,
              confidence: 'high',
              event_count: 3,
              last_event: 'error',
              metrics: {
                elapsed_seconds: 50,
                total_tokens: 950,
                model_name: 'gpt-5.4-codex',
              },
            },
          ],
        },
      ],
    },
  ],
  provider_costs: {
    anthropic: { day: 12.5, week: 50, month: 120, year: 900 },
    openai: { day: 3.25, week: 9, month: 30, year: 200 },
    unattributed: { day: 0.75, week: 1, month: 2, year: 3 },
  },
};
