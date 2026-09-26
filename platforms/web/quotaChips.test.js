import { describe, expect, test } from 'vitest';
import {
  quotaWindowLabel, providerKeyFor, usageCreditsLine, providerIconHTML,
  quotaChipLayout, buildQuotaRowDOM, buildOverflowChipDOM,
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
      { key: 'openai', mode: 'subscription', snapshot: {}, imminent: { used_percent: 90 } },
      { key: 'zai', mode: 'subscription', snapshot: {}, imminent: null },
    ];
    const pill = buildOverflowChipDOM(hidden);
    expect(pill.textContent).toBe('+2 more');
    expect(pill.title).toBe('Openai: 90%\nZai');
  });
});
