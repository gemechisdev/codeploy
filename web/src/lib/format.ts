const RTF = typeof Intl !== 'undefined' ? new Intl.RelativeTimeFormat('en', { numeric: 'auto' }) : null;

export function relTime(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return '—';
  const s = Math.round((Date.parse(iso) - now) / 1000);
  const a = Math.abs(s);
  if (a < 45) return s <= 0 ? 'just now' : 'in a few seconds';
  const units: [Intl.RelativeTimeFormatUnit, number][] = [['minute', 60], ['hour', 3600], ['day', 86400], ['month', 2_592_000]];
  let u: Intl.RelativeTimeFormatUnit = 'minute'; let div = 60;
  for (const [n, d] of units) if (a >= d) { u = n; div = d; }
  return RTF ? RTF.format(Math.round(s / div), u) : `${Math.round(a / div)} ${u}s`;
}
export const absTime = (iso: string | null | undefined) => (iso ? new Date(iso).toLocaleString() : '—');
export function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const u = ['KB', 'MB', 'GB', 'TB']; let i = -1; let v = n;
  do { v /= 1024; i++; } while (v >= 1024 && i < u.length - 1);
  return `${v.toFixed(v < 10 ? 1 : 0)} ${u[i]}`;
}
/** 125 -> "2m 05s" */
export function duration(totalSeconds: number): string {
  const s = Math.max(0, Math.floor(totalSeconds));
  const h = Math.floor(s / 3600); const m = Math.floor((s % 3600) / 60); const sec = s % 60;
  return h ? `${h}h ${String(m).padStart(2, '0')}m` : m ? `${m}m ${String(sec).padStart(2, '0')}s` : `${sec}s`;
}
export const shortDigest = (d: string | null | undefined) => (d ? d.replace('sha256:', '').slice(0, 12) : '—');
export const pct = (n: number) => `${Math.round(n)}%`;
