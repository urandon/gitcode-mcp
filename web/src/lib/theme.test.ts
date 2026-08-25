import { describe, expect, it } from 'vitest';
import { normalizeTheme } from './theme';

describe('normalizeTheme', () => {
  it('defaults missing and unknown values to system', () => {
    expect(normalizeTheme(null)).toBe('system');
    expect(normalizeTheme('sepia')).toBe('system');
  });

  it('keeps explicit light and dark choices', () => {
    expect(normalizeTheme('light')).toBe('light');
    expect(normalizeTheme('dark')).toBe('dark');
  });
});
