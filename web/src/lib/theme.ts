export type Theme = 'light' | 'dark' | 'system';

export const themeStorageKey = 'gitcode-mcp-theme';

export function normalizeTheme(value: string | null): Theme {
  return value === 'light' || value === 'dark' ? value : 'system';
}

export function applyTheme(theme: Theme): void {
  document.documentElement.dataset.theme = theme;
  localStorage.setItem(themeStorageKey, theme);
}
