import { ArrowDownToLine, Pause, Play, Search } from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { cn } from '@/lib/cn';

export interface LogRow { key: string | number; at?: string; stream?: 'stdout' | 'stderr' | 'system'; text: string }
const ROW = 18; const OVERSCAN = 20; export const LOG_CAP = 5000;

/** Virtualized: only the visible window is rendered, so 5,000 lines stay cheap. Auto-follow, pause, filter. */
export function LogViewer({ rows, height = 340, emptyText = 'No log output yet', live = false }: { rows: LogRow[]; height?: number; emptyText?: string; live?: boolean }) {
  const [filter, setFilter] = useState('');
  const [paused, setPaused] = useState(false);
  const [follow, setFollow] = useState(true);
  const [top, setTop] = useState(0);
  const box = useRef<HTMLDivElement>(null);
  const shown = useMemo(() => {
    const q = filter.trim().toLowerCase();
    const list = q ? rows.filter((r) => r.text.toLowerCase().includes(q)) : rows;
    return list.length > LOG_CAP ? list.slice(-LOG_CAP) : list;
  }, [rows, filter]);
  const frozen = useRef(shown);
  if (!paused) frozen.current = shown;
  const data = paused ? frozen.current : shown;

  useEffect(() => {
    const el = box.current;
    if (el && follow && !paused) el.scrollTop = el.scrollHeight;
  }, [data.length, follow, paused]);

  const start = Math.max(0, Math.floor(top / ROW) - OVERSCAN);
  const end = Math.min(data.length, Math.ceil((top + height) / ROW) + OVERSCAN);
  return (
    <div className="overflow-hidden rounded-lg border border-subtle bg-code-bg">
      <div className="flex flex-wrap items-center gap-2 border-b border-white/10 px-2 py-1.5">
        <div className="relative min-w-32 flex-1">
          <Search aria-hidden className="pointer-events-none absolute left-2 top-1/2 size-3 -translate-y-1/2 text-zinc-500" />
          <Input aria-label="Filter log lines" placeholder="Filter…" value={filter} onChange={(e) => setFilter(e.target.value)} className="border-white/10 bg-white/5 pl-7 text-zinc-200" />
        </div>
        <Button variant="ghost" size="sm" className="text-zinc-300" onClick={() => setPaused((p) => !p)} aria-pressed={paused}>{paused ? <Play className="size-3" /> : <Pause className="size-3" />}{paused ? 'Resume' : 'Pause'}</Button>
        <Button variant="ghost" size="sm" className="text-zinc-300" onClick={() => { setFollow(true); setPaused(false); }} aria-pressed={follow}><ArrowDownToLine className="size-3" />Follow</Button>
        <span className="font-mono text-[10px] text-zinc-500" aria-live="off">{live ? '● live · ' : ''}{data.length}{rows.length >= LOG_CAP ? ' (capped at 5,000)' : ''} lines</span>
      </div>
      <div ref={box} role="log" aria-label="Log output" tabIndex={0} style={{ height }} className="overflow-auto font-mono text-[11px] leading-[18px]"
        onScroll={(e) => { const el = e.currentTarget; setTop(el.scrollTop); setFollow(el.scrollHeight - el.scrollTop - el.clientHeight < 24); }}>
        {data.length === 0 ? <p className="p-3 text-zinc-500">{emptyText}</p> : (
          <div style={{ height: data.length * ROW, position: 'relative' }}>
            {data.slice(start, end).map((r, i) => (
              <div key={r.key} style={{ position: 'absolute', top: (start + i) * ROW, height: ROW }} className={cn('flex w-full gap-2 whitespace-pre px-3', r.stream === 'stderr' ? 'text-red-300' : r.stream === 'system' ? 'text-brand-accent' : 'text-zinc-300')}>
                {r.at && <span className="shrink-0 text-zinc-600">{r.at.slice(11, 19)}</span>}<span>{r.text}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
