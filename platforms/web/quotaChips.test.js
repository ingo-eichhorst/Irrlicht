import { describe, expect, test } from 'vitest';
import { quotaWindowLabel } from './quotaChips.js';

describe('quotaWindowLabel', () => {
  test('labels a Codex single-weekly snapshot without inferring a five-hour window', () => {
    expect(quotaWindowLabel(10080)).toBe('7d');
  });

  test('continues to label a reported five-hour window', () => {
    expect(quotaWindowLabel(300)).toBe('5h');
  });
});
