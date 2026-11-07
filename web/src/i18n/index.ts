import { createContext, createElement, useContext, useMemo, type ReactNode } from 'react';
import { en, type Dictionary, type TranslationKey } from './en';

export { en } from './en';
export type { Dictionary, TranslationKey } from './en';

export type Translate = (key: TranslationKey) => string;

/** Default-locale lookup for non-React call sites (and the context fallback). */
export const t: Translate = (key) => en[key];

const I18nContext = createContext<Translate>(t);

/** Supply a different dictionary later without changing any call site. */
export function I18nProvider({ children, dictionary = en }: { children: ReactNode; dictionary?: Dictionary }) {
  const value = useMemo<Translate>(() => (key) => dictionary[key] ?? en[key], [dictionary]);
  return createElement(I18nContext.Provider, { value }, children);
}

export const useT = (): Translate => useContext(I18nContext);
