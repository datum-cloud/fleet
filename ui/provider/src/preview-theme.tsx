// Light/dark switching for the standalone preview harness only. Federated
// pages never import this: staff-portal owns the theme there.
//
// `?theme=light|dark` on any preview URL wins and is remembered; otherwise the
// last choice, then the system preference. Datum UI's dark variant keys off a
// `.dark` class on an ancestor, so the class goes on <html>.

import { Button } from '@datum-cloud/datum-ui/button';
import { useState } from 'react';

export type PreviewTheme = 'light' | 'dark';
const STORAGE_KEY = 'fleet-preview-theme';

function storedTheme(): PreviewTheme | undefined {
  try {
    const value = localStorage.getItem(STORAGE_KEY);
    return value === 'light' || value === 'dark' ? value : undefined;
  } catch {
    return undefined;
  }
}

export function initialPreviewTheme(search = window.location.search): PreviewTheme {
  const requested = new URLSearchParams(search).get('theme');
  if (requested === 'light' || requested === 'dark') return requested;
  return storedTheme() ?? (window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
}

export function applyPreviewTheme(theme: PreviewTheme) {
  document.documentElement.classList.toggle('dark', theme === 'dark');
  document.documentElement.style.colorScheme = theme;
  try { localStorage.setItem(STORAGE_KEY, theme); } catch { /* storage unavailable */ }
}

export function PreviewThemeToggle() {
  const [theme, setTheme] = useState<PreviewTheme>(() => document.documentElement.classList.contains('dark') ? 'dark' : 'light');
  const next: PreviewTheme = theme === 'dark' ? 'light' : 'dark';
  return (
    <Button type="secondary" theme="outline" size="small" htmlType="button" aria-label={`Switch to ${next} theme`} onClick={() => { applyPreviewTheme(next); setTheme(next); }}>
      {theme === 'dark' ? 'Light theme' : 'Dark theme'}
    </Button>
  );
}
