import { cn } from '@/lib/cn';

/** Labelled bar gauge. Colour shifts at warn/crit, and the numeric value is always printed (colour is never the only signal). */
export function Gauge({ label, value, max = 100, unit = '%', detail, warn = 70, crit = 90 }: { label: string; value: number; max?: number; unit?: string; detail?: string; warn?: number; crit?: number }) {
  const pct = Math.min(100, Math.max(0, (value / max) * 100));
  const tone = pct >= crit ? 'bg-red-500' : pct >= warn ? 'bg-amber-500' : 'bg-brand-primary';
  return (
    <div className="space-y-1.5" role="group" aria-label={`${label}: ${Math.round(value)}${unit}${detail ? `, ${detail}` : ''}`}>
      <div className="flex items-baseline justify-between"><span className="text-[11px] font-semibold text-secondary">{label}</span><span className="text-xs font-bold tabular-nums text-primary">{Math.round(value)}{unit}</span></div>
      <div className="h-1.5 overflow-hidden rounded-full bg-surface-muted"><div className={cn('h-full rounded-full transition-[width] duration-500 motion-reduce:transition-none', tone)} style={{ width: `${pct}%` }} /></div>
      {detail && <p className="text-[10px] tabular-nums text-muted">{detail}</p>}
    </div>
  );
}
