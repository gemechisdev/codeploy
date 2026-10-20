import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';

export const THEME_KEY = 'codeplo_theme';
type Theme = 'dark' | 'light';
const Ctx = createContext<{ theme: Theme; toggle: () => void }>({ theme: 'dark', toggle: () => {} });

function read(): Theme {
  try { return localStorage.getItem(THEME_KEY) === 'light' ? 'light' : 'dark'; } catch { return 'dark'; }
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setTheme] = useState<Theme>(read);
  useEffect(() => {
    document.documentElement.classList.toggle('dark', theme === 'dark');
    try { localStorage.setItem(THEME_KEY, theme); } catch { /* storage unavailable */ }
  }, [theme]);
  const toggle = useCallback(() => setTheme((t) => (t === 'dark' ? 'light' : 'dark')), []);
  return <Ctx.Provider value={useMemo(() => ({ theme, toggle }), [theme, toggle])}>{children}</Ctx.Provider>;
}
export const useTheme = () => useContext(Ctx);
