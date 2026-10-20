import 'uplot/dist/uPlot.min.css';
import uPlot from 'uplot';
import { useEffect, useRef, useState } from 'react';
import { useMetrics } from '@/api/hooks/core';
import type { HostMetrics } from '@/api/types';
import { QueryBoundary } from '@/components/app/QueryBoundary';
import { Card, CardBody, CardHeader } from '@/components/ui/card';
import { cn } from '@/lib/cn';
import { ModuleGate, PageHeader } from '@/features/shell/ModuleGate';

const RANGES = ['15m', '1h', '6h', '24h'] as const;

function Chart({ title, points, series, unit, max }: { title: string; points: HostMetrics[]; series: { label: string; color: string; pick: (p: HostMetrics) => number }[]; unit: string; max?: number }) {
  const el = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!el.current || points.length === 0) return;
    const x = points.map((p) => Date.parse(p.at) / 1000);
    const data: uPlot.AlignedData = [x, ...series.map((s) => points.map(s.pick))];
    const cs = getComputedStyle(document.documentElement);
    const grid = cs.getPropertyValue('--border-subtle').trim() || '#382054'; const axis = cs.getPropertyValue('--text-muted').trim() || '#8d73a3';
    const plot = new uPlot({
      width: el.current.clientWidth, height: 180, padding: [8, 8, 0, 0], legend: { show: false }, cursor: { drag: { x: false, y: false } },
      scales: { y: { range: (_u, _lo, hi) => [0, max ?? Math.max(hi * 1.15, 1)] } },
      axes: [{ stroke: axis, grid: { stroke: grid, width: 1 }, ticks: { show: false } }, { stroke: axis, grid: { stroke: grid, width: 1 }, ticks: { show: false }, size: 46, values: (_u, v) => v.map((n) => `${Math.round(n)}${unit}`) }],
      series: [{}, ...series.map((s) => ({ label: s.label, stroke: s.color, width: 1.75, fill: `${s.color}22`, points: { show: false } }))],
    }, data, el.current);
    const ro = new ResizeObserver(() => el.current && plot.setSize({ width: el.current.clientWidth, height: 180 }));
    ro.observe(el.current);
    return () => { ro.disconnect(); plot.destroy(); };
  }, [points, series, unit, max]);
  const last = points.at(-1);
  return (
    <Card><CardHeader title={title} description={last && series.map((s) => `${s.label} ${Math.round(s.pick(last))}${unit}`).join(' · ')} />
      <CardBody><div ref={el} role="img" aria-label={`${title} over time`} className="w-full" />
        {/* The values are also available as text for assistive tech */}
        <p className="sr-only">Latest {series.map((s) => last ? `${s.label}: ${Math.round(s.pick(last))}${unit}` : '').join(', ')}</p></CardBody></Card>
  );
}

export function MonitoringPage() {
  const [range, setRange] = useState<(typeof RANGES)[number]>('1h');
  const q = useMetrics(range);
  return (
    <ModuleGate module="monitor">
      <PageHeader title="Monitoring" description="Host metrics sampled by the daemon." actions={
        <div role="group" aria-label="Time range" className="flex rounded-lg border border-subtle bg-surface-subtle p-0.5">{RANGES.map((r) => <button key={r} aria-pressed={r === range} onClick={() => setRange(r)} className={cn('cursor-pointer rounded-md px-2.5 py-1 text-xs font-semibold', r === range ? 'bg-brand-primary text-white' : 'text-secondary hover:text-primary')}>{r}</button>)}</div>} />
      <QueryBoundary query={q} isEmpty={(d) => d.points.length === 0} rows={4}>
        {(d) => (
          <div className="grid gap-4 lg:grid-cols-2">
            <Chart title="CPU" unit="%" max={100} points={d.points} series={[{ label: 'CPU', color: '#A43CC7', pick: (p) => p.cpu_percent }]} />
            <Chart title="Memory" unit=" MB" points={d.points} series={[{ label: 'Used', color: '#62D6C3', pick: (p) => p.memory_used_mb }, { label: 'Daemon', color: '#A43CC7', pick: (p) => p.daemon_rss_mb }]} />
            <Chart title="Disk" unit=" GB" points={d.points} series={[{ label: 'Used', color: '#e0a526', pick: (p) => p.disk_used_gb }]} />
            <Chart title="Network" unit=" kbps" points={d.points} series={[{ label: 'RX', color: '#62D6C3', pick: (p) => p.net_rx_kbps ?? 0 }, { label: 'TX', color: '#A43CC7', pick: (p) => p.net_tx_kbps ?? 0 }]} />
          </div>)}
      </QueryBoundary>
    </ModuleGate>
  );
}
