import { describe, expect, test } from 'vitest';
import {
  quotaWindowLabel, providerKeyFor, usageCreditsLine, providerIconHTML,
  quotaChipLayout, buildQuotaRowDOM, buildOverflowChipDOM, applyStripDensity,
  bucketChips, productLabel, quotaTooltip, providerRowChips,
} from './quotaChips.js';

describe('quotaWindowLabel', () => {
  test('labels a Codex single-weekly snapshot without inferring a five-hour window', () => {
    expect(quotaWindowLabel(10080)).toBe('7d');
  });

  test('continues to label a reported five-hour window', () => {
    expect(quotaWindowLabel(300)).toBe('5h');
  });
});

// Red-first coverage for issue #1995: providerKeyFor infers a billing
// identity from plan_type/adapter today, which misbrands any non-Anthropic
// provider whose tier happens to be called "pro" (several providers sell a
// tier by that name — e.g. GitHub Copilot Pro).
describe('providerKeyFor', () => {
  test('does not brand a non-Anthropic pro-tier snapshot as Anthropic', () => {
    const snap = { plan_type: 'pro', windows: [] };
    expect(providerKeyFor(snap, 'copilot')).not.toBe('anthropic');
  });
});

// Red-first coverage for issue #1995: usageCreditsLine hardcodes a "$"
// prefix today, which is wrong for any provider balance denominated in a
// non-USD currency (e.g. DeepSeek's CNY balances).
describe('usageCreditsLine', () => {
  test('renders a non-dollar balance in its own currency, not $', () => {
    const line = usageCreditsLine({ has_credits: true, balance: 12.34, currency: 'CNY' });
    expect(line).not.toContain('$');
  });

  // Issue #2009 — LOCKS, not red-first: these pass on the #1995 renderer
  // unchanged. They pin the wire shapes core/adapters/outbound/
  // deepseekaccountapi produces (balance omitted by omitempty on a zero,
  // balance_observed kept) and the four states #2009 §1.3 requires to stay
  // apart. QuotaChipModeTests.swift (lines 19-71) pins the same four states on macOS.
  test('keeps observed zero, unknown, unlimited and missing distinct', () => {
    const zero = usageCreditsLine({ has_credits: false, balance_observed: true, currency: 'CNY', total: 0, granted: 0, topped_up: 0 });
    const unknown = usageCreditsLine({ has_credits: true });
    const unlimited = usageCreditsLine({ has_credits: true, unlimited: true });
    const missing = usageCreditsLine(null);
    expect(zero).toBe('Credits balance: 0.00 CNY');
    expect(unknown).toBe('Credits: available');
    expect(unlimited).toBe('Credits: unlimited');
    expect(missing).toBeNull();
    expect(new Set([zero, unknown, unlimited, missing]).size).toBe(4);
  });

  test('renders a DeepSeek CNY balance in CNY', () => {
    const line = usageCreditsLine({ has_credits: true, balance: 110, balance_observed: true, currency: 'CNY', total: 110, granted: 10, topped_up: 100 });
    expect(line).toBe('Credits balance: 110.00 CNY');
  });
});

// Issue #2057: the daemon stamps Muse Code's account-API snapshots with
// provider "meta", so a Meta chip needs a bundled mark rather than the
// generic-circle fallback. Seen red before the meta entry existed
// (providerIconHTML('meta') returned '').
describe('providerIconHTML', () => {
  test('bundles a Meta mark for the meta provider key', () => {
    const svg = providerIconHTML('meta');
    expect(svg).toContain('<svg');
    expect(svg).toContain('fill="currentColor"');
  });

  test('returns empty for an unknown provider so the caller falls back', () => {
    expect(providerIconHTML('nonexistent-provider')).toBe('');
  });
});

// Issue #2063: the header capped quota chips at two, so a third provider
// (Meta, since #2058) pushed OpenAI into a "+1 more" pill. Same table as
// QuotaChipLayoutTests.swift on macOS. Counts 0-2 are locks (unchanged since
// before #2063); 3..7 were seen red against the cap-2 extraction.
describe('quotaChipLayout', () => {
  test.each([
    [0, 0, 0, 'regular'],
    [1, 1, 0, 'regular'],
    [2, 2, 0, 'compact'],
    [3, 3, 0, 'tight'],
    [4, 4, 0, 'dense'],
    [5, 5, 0, 'dense'],
    // Past five, the pill takes a chip's slot (see quotaChipLayout).
    [6, 4, 2, 'dense'],
    [7, 4, 3, 'dense'],
  ])('%i providers -> %i inline, %i in the overflow pill, %s', (count, visible, overflow, density) => {
    expect(quotaChipLayout(count)).toEqual({ visible, overflow, density });
  });
});

describe('buildQuotaRowDOM per density', () => {
  // resets_at is unix seconds on the wire; an hour out keeps the pace marker in range.
  const nowMs = Date.UTC(2026, 8, 26, 17, 0, 0);
  const w = { window_minutes: 300, used_percent: 42, resets_at: nowMs / 1000 + 3600 };
  const parts = (row) => Array.from(row.children).map((c) => c.className.split(' ')[0]);

  test('regular keeps the window label and the inline reset time', () => {
    expect(parts(buildQuotaRowDOM(w, 'regular', nowMs)))
      .toEqual(['quota-row-label', 'quota-bar', 'quota-row-percent', 'quota-row-reset']);
  });

  test.each(['compact', 'tight'])('%s drops only the reset time', (density) => {
    expect(parts(buildQuotaRowDOM(w, density, nowMs)))
      .toEqual(['quota-row-label', 'quota-bar', 'quota-row-percent']);
  });

  test('dense also drops the window label but keeps the percent', () => {
    const row = buildQuotaRowDOM(w, 'dense', nowMs);
    expect(parts(row)).toEqual(['quota-bar', 'quota-row-percent']);
    expect(row.querySelector('.quota-row-percent').textContent).toBe('42%');
  });
});

describe('buildOverflowChipDOM', () => {
  test('the pill carries its "+N more" label', () => {
    const hidden = [
      { key: 'openai|session:a', providerKey: 'openai', accountRef: '', mode: 'subscription', snapshot: {}, imminent: { used_percent: 90 } },
      { key: 'zai|session:b', providerKey: 'zai', accountRef: 'zzzz-1234', mode: 'subscription', snapshot: {}, imminent: null },
    ];
    const pill = buildOverflowChipDOM(hidden);
    expect(pill.textContent).toBe('+2 more');
    expect(pill.title).toBe('Openai · Account unconfirmed: 90%\nZai · Account …1234');
  });
});

// The per-tier CSS only applies through the strip's class (review finding on
// PR #2065: nothing exercised it). Each count's layout goes through the real
// quotaChipLayout, so the table above and the CSS selectors are connected.
describe('applyStripDensity', () => {
  const stripClasses = (el) => Array.from(el.classList).filter((c) => c.startsWith('quota-chips--'));

  test.each([
    [1, ['quota-chips--regular']],
    [2, ['quota-chips--compact']],
    [3, ['quota-chips--tight', 'quota-chips--flex']],
    [5, ['quota-chips--dense', 'quota-chips--flex']],
    [6, ['quota-chips--dense', 'quota-chips--flex']],
  ])('%i providers put %j on the strip', (count, want) => {
    const el = document.createElement('div');
    applyStripDensity(el, quotaChipLayout(count).density);
    expect(stripClasses(el)).toEqual(want);
  });

  test('a new density replaces the previous one, and null clears it', () => {
    const el = document.createElement('div');
    el.classList.add('quota-chips');
    applyStripDensity(el, 'dense');
    applyStripDensity(el, 'tight');
    expect(stripClasses(el)).toEqual(['quota-chips--tight', 'quota-chips--flex']);
    applyStripDensity(el, null);
    expect(Array.from(el.classList)).toEqual(['quota-chips']);
  });
});

// Issue #2095: one chip per confirmed account; an unconfirmed session never
// merges with another session's snapshot.
describe('bucketChips per account (#2095)', () => {
  const nowMs = Date.UTC(2026, 0, 1);
  const future = nowMs / 1000 + 3600;
  const mk = (id, adapter, extra, sampledAt, pct) => ({
    session_id: id, adapter,
    metrics: { rate_limit: {
      attribution_quality: 'confirmed', provider: 'anthropic',
      sampled_at: sampledAt, windows: [{ used_percent: pct, window_minutes: 300, resets_at: future }],
      ...extra,
    } },
  });
  const pctOf = (chip) => chip.snapshot.windows[0].used_percent;

  test('C1: two confirmed accounts of one provider stay two chips across interleaved ticks', () => {
    for (const [aT, bT] of [[2, 1], [3, 4], [6, 5], [7, 8]]) {
      const chips = bucketChips([
        mk('s1', 'claude-code', { confirmed_account_ref: 'acct-A' }, aT, 10),
        mk('s2', 'claude-code', { confirmed_account_ref: 'acct-B' }, bT, 20),
      ], nowMs);
      expect(chips).toHaveLength(2);
      expect(pctOf(chips.find((c) => c.accountRef === 'acct-A'))).toBe(10);
      expect(pctOf(chips.find((c) => c.accountRef === 'acct-B'))).toBe(20);
      expect(chips.every((c) => c.providerKey === 'anthropic')).toBe(true);
    }
  });

  test('C2: confirmed provider with empty or missing ref never merges across sessions', () => {
    for (const [aT, bT] of [[2, 1], [3, 4], [6, 5]]) {
      const chips = bucketChips([
        mk('s1', 'claude-code', { confirmed_account_ref: '' }, aT, 10),
        mk('s2', 'claude-code', {}, bT, 20),
      ], nowMs);
      expect(chips).toHaveLength(2);
      expect(chips.map(pctOf).sort()).toEqual([10, 20]);
      expect(chips.every((c) => c.accountRef === '')).toBe(true);
    }
  });

  test('C2: two unknown-provider sessions of one adapter stay two chips', () => {
    const unk = (id, t, pct) => ({
      session_id: id, adapter: 'codex',
      metrics: { rate_limit: { sampled_at: t, windows: [{ used_percent: pct, window_minutes: 300, resets_at: future }] } },
    });
    for (const [aT, bT] of [[2, 1], [3, 4]]) {
      const chips = bucketChips([unk('u1', aT, 10), unk('u2', bT, 20)], nowMs);
      expect(chips).toHaveLength(2);
      expect(chips.map(pctOf).sort()).toEqual([10, 20]);
      expect(chips.every((c) => c.providerKey === 'unknown:codex')).toBe(true);
    }
  });

  test('same account on two sessions still folds to one chip (newest wins)', () => {
    const chips = bucketChips([
      mk('s1', 'claude-code', { confirmed_account_ref: 'acct-A' }, 1, 10),
      mk('s2', 'claude-code', { confirmed_account_ref: 'acct-A' }, 2, 30),
    ], nowMs);
    expect(chips).toHaveLength(1);
    expect(pctOf(chips[0])).toBe(30);
  });

  // C3 LOCKS (see run notes): the stored preference stays keyed by the bare provider.
  test('C3 lock: providerMode_anthropic still governs an account bucket', () => {
    localStorage.setItem('providerMode_anthropic', 'usage');
    try {
      const chips = bucketChips([
        mk('s1', 'claude-code', { confirmed_account_ref: 'acct-A', credits: { has_credits: true } }, 1, 10),
      ], nowMs);
      expect(chips).toHaveLength(1);
      expect(chips[0].mode).toBe('usage');
    } finally { localStorage.removeItem('providerMode_anthropic'); }
  });

  test('C3 lock: settings rows stay one per provider with two account buckets', () => {
    const chips = bucketChips([
      mk('s1', 'claude-code', { confirmed_account_ref: 'acct-A' }, 1, 10),
      mk('s2', 'claude-code', { confirmed_account_ref: 'acct-B' }, 2, 20),
    ], nowMs);
    expect(providerRowChips(chips)).toHaveLength(1);
  });

  test('C4: tooltip and overflow name the account; productLabel maps known products', () => {
    expect(productLabel('claude_team')).toBe('Claude Team');
    expect(productLabel('some_new_tier')).toBe('Some New Tier');
    const [a] = bucketChips([mk('s1', 'claude-code', { confirmed_account_ref: 'acct-ABCD', product: 'claude_team' }, 1, 10)], nowMs);
    expect(quotaTooltip(a, nowMs).split('\n').slice(0, 2)).toEqual(['Claude Team', 'Account …ABCD']);
    const [u] = bucketChips([mk('s2', 'claude-code', {}, 1, 10)], nowMs);
    expect(quotaTooltip(u, nowMs)).toContain('Account unconfirmed');
    expect(buildOverflowChipDOM([a, u]).title).toBe('Claude Team · Account …ABCD: 10%\nAnthropic · Account unconfirmed: 10%');
  });
});
