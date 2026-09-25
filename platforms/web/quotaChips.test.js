import { describe, expect, test } from 'vitest';
import { quotaWindowLabel, providerKeyFor, usageCreditsLine, providerIconHTML } from './quotaChips.js';

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
