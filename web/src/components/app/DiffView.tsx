import type { components } from '@/api/schema';
import { cn } from '@/lib/cn';

type PlanDiff = components['schemas']['PlanDiff'];

/** Exact before/after for a planned change. Read-only `<pre>`: no editor needed (CodeMirror deferred). */
export function DiffView({ diff }: { diff: PlanDiff }) {
  const before = diff.before.split('\n'); const after = diff.after.split('\n');
  return (
    <div className="overflow-hidden rounded-lg border border-subtle bg-code-bg text-[11px] font-mono">
      <div className="border-b border-white/10 px-3 py-1.5 text-zinc-400">{diff.target}</div>
      <div className="max-h-72 overflow-auto py-1.5">
        {before.map((l, i) => <Line key={`b${i}`} sign="-" text={l} />)}
        {after.map((l, i) => <Line key={`a${i}`} sign="+" text={l} />)}
      </div>
    </div>
  );
}
function Line({ sign, text }: { sign: '+' | '-'; text: string }) {
  return (
    <div className={cn('flex gap-2 px-3 py-px whitespace-pre', sign === '+' ? 'bg-emerald-500/10 text-emerald-300' : 'bg-red-500/10 text-red-300')}>
      <span aria-hidden className="select-none">{sign}</span><span className="sr-only">{sign === '+' ? 'added: ' : 'removed: '}</span><span>{text}</span>
    </div>
  );
}
