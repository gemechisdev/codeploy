import { useEffect, useState } from 'react';
import { serverNowMs } from '@/api/session';

/** Re-renders every `ms` and returns the SERVER clock (offset-corrected), never the raw client clock. */
export function useServerNow(ms = 1000): number {
  const [now, setNow] = useState(serverNowMs);
  useEffect(() => {
    const t = setInterval(() => setNow(serverNowMs()), ms);
    return () => clearInterval(t);
  }, [ms]);
  return now;
}

/** Seconds remaining until `iso` by the server clock; null when there is no deadline. */
export function useCountdown(iso: string | null | undefined): number | null {
  const now = useServerNow(250);
  return iso ? Math.max(0, Math.ceil((Date.parse(iso) - now) / 1000)) : null;
}
