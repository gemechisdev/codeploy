import qrcode from 'qrcode-generator';
import { useMemo } from 'react';

/** Renders the QR as SVG rects (no innerHTML). Quiet zone of 4 modules is part of the viewBox. */
export function QrCode({ value, label }: { value: string; label: string }) {
  const cells = useMemo(() => {
    const qr = qrcode(0, 'M');
    qr.addData(value);
    qr.make();
    const n = qr.getModuleCount();
    const out: string[] = [];
    for (let y = 0; y < n; y++) for (let x = 0; x < n; x++) if (qr.isDark(y, x)) out.push(`M${x + 4} ${y + 4}h1v1h-1z`);
    return { d: out.join(''), size: n + 8 };
  }, [value]);
  return (
    <svg role="img" aria-label={label} viewBox={`0 0 ${cells.size} ${cells.size}`} className="size-44 rounded-lg bg-white" shapeRendering="crispEdges">
      <path d={cells.d} fill="#1e1030" />
    </svg>
  );
}
