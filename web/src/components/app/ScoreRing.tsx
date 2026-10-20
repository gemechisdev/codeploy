export function ScoreRing({ score, size = 96, label = 'Security score' }: { score: number; size?: number; label?: string }) {
  const r = (size - 12) / 2; const c = 2 * Math.PI * r;
  const tone = score >= 80 ? 'text-emerald-500' : score >= 50 ? 'text-amber-500' : 'text-red-500';
  return (
    <div role="img" aria-label={`${label}: ${score} out of 100`} className={`relative shrink-0 ${tone}`} style={{ width: size, height: size }}>
      <svg width={size} height={size} className="-rotate-90">
        <circle cx={size / 2} cy={size / 2} r={r} fill="none" strokeWidth={8} className="stroke-surface-muted" />
        <circle cx={size / 2} cy={size / 2} r={r} fill="none" strokeWidth={8} strokeLinecap="round" stroke="currentColor" strokeDasharray={c} strokeDashoffset={c * (1 - score / 100)} className="transition-[stroke-dashoffset] duration-500 motion-reduce:transition-none" />
      </svg>
      <div className="absolute inset-0 flex flex-col items-center justify-center"><span className="text-xl font-bold tabular-nums text-primary">{score}</span><span className="text-[9px] uppercase tracking-wider text-muted">/ 100</span></div>
    </div>
  );
}
