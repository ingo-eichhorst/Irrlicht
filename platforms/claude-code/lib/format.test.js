import { describe, expect, test } from 'vitest';
import { fitRow, fmtDuration, formatTokens, formatUsd, oneLine, quotaBar, shortModel } from './format.js';
import { barTone, formatTimeUntil, quotaWindowLabel } from './quota.js';

describe('formatting', () => {
  test('tokens', () => {
    expect([0, 950, 4200, 66_000, 177_400, 1_250_000, 12_400_000].map(formatTokens))
      .toEqual(['', '950', '4.2K', '66K', '177K', '1.3M', '12M']);
  });

  test('models', () => {
    expect(['claude-opus-5-5', 'claude-sonnet-4-5-20250929', 'gpt-5.4', 'unknown', '', 'kimi-k2']
      .map(shortModel)).toEqual(['opus-5.5', 'sonnet-4.5', 'gpt-5.4', '', '', 'kimi-k2']);
  });

  test('durations, as the dashboard prints them', () => {
    expect([0, 59, 61, 3725, undefined].map(fmtDuration)).toEqual(['0s', '59s', '1m1s', '1h2m', '']);
  });

  test('money, as the dashboard prints usage cost', () => {
    expect([0, 0.004, 12.345, 123.6].map(formatUsd)).toEqual(['$0', '<$0.01', '$12.35', '$124']);
  });

  test('one line, cut by code point', () => {
    expect(oneLine('a\n\n b\tc ', 10)).toBe('a b c');
    expect(oneLine('😀'.repeat(5), 3)).toBe('😀😀…');
  });

  test('quota bars', () => {
    expect(quotaBar(11, 10)).toBe('█░░░░░░░░░');
    expect(quotaBar(83, 10)).toBe('████████░░');
    expect(quotaBar(150, 4)).toBe('████');
  });

  test('quota helpers ported from the dashboard', () => {
    expect([300, 299, 10080, 1440, 120, 30].map(quotaWindowLabel)).toEqual(['5h', '5h', '7d', '1d', '2h', '30m']);
    expect([barTone(90, null), barTone(72, null), barTone(55, null), barTone(10, null)])
      .toEqual(['orange', 'orange', 'yellow', 'green']);
    expect([barTone(40, 20), barTone(40, 34), barTone(40, 40)]).toEqual(['orange', 'yellow', 'green']);
    const now = 1_000_000_000_000;
    expect(formatTimeUntil(now / 1000 + 2 * 3600 + 14 * 60, now)).toBe('2h 14m');
    expect(formatTimeUntil(now / 1000 + 3 * 86400 + 4 * 3600, now)).toBe('3d 4h');
    expect(formatTimeUntil(now / 1000 - 5, now)).toBe('0m');
  });
});

describe('fitRow', () => {
  const row = {
    branch: 'feature/a-very-long-branch-name', tokens: '177K', context: '17.7%', model: 'opus-5.5',
    adapterTag: '[pi]', isHere: true,
  };

  test('everything fits a wide pane', () => {
    expect(fitRow(row, 80)).toBe('feature/a-very-long-branch-name 177K 17.7% opus-5.5 [pi] ← here');
  });

  test('the branch is shortened first', () => {
    expect(fitRow(row, 44)).toBe('feature/a… 177K 17.7% opus-5.5 [pi] ← here');
    expect(fitRow(row, 40)).toBe('featu… 177K 17.7% opus-5.5 [pi] ← here');
  });

  test('then the model, then the context, then the tokens; the tag and the marker stay', () => {
    expect(fitRow(row, 36)).toBe('feature/a-… 177K 17.7% [pi] ← here');
    expect(fitRow(row, 30)).toBe('feature/a-… 177K [pi] ← here');
    expect(fitRow(row, 24)).toBe('feature/a… [pi] ← here');
  });

  test('the text never exceeds the room after the dot while the tag and marker fit', () => {
    for (let width = 16; width <= 80; width++) {
      expect(fitRow(row, width).length, 'width ' + width).toBeLessThanOrEqual(width - 2);
    }
  });

  test('a row with no branch shows a dash', () => {
    expect(fitRow({ tokens: '1K' }, 40)).toBe('— 1K');
  });
});
