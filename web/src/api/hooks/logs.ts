import { useEffect, useRef, useState } from 'react';
import { API_BASE } from '../client';
import type { DeploymentLogLine } from '../types';
import type { LogRow } from '@/components/app/LogViewer';
import { LOG_CAP } from '@/components/app/LogViewer';

/** Opens one SSE stream and exposes its lines. Batches renders (~8/s) so a chatty build cannot starve the UI. */
function useLogStream(url: string | null, parse: (data: string, id: string, n: number) => LogRow | null) {
  const [rows, setRows] = useState<LogRow[]>([]);
  const [live, setLive] = useState(false);
  const [ended, setEnded] = useState(false);
  const parseRef = useRef(parse);
  parseRef.current = parse;
  useEffect(() => {
    setRows([]); setEnded(false); setLive(false);
    if (!url) return;
    const buf: LogRow[] = [];
    let n = 0; let seen = new Set<string | number>();
    const flush = () => {
      if (!buf.length) return;
      const add = buf.splice(0);
      setRows((r) => { const next = r.concat(add); return next.length > LOG_CAP ? next.slice(-LOG_CAP) : next; });
    };
    const timer = setInterval(flush, 120);
    const es = new EventSource(url);
    es.onopen = () => setLive(true);
    es.onmessage = (e) => {
      const row = parseRef.current(e.data, e.lastEventId, ++n);
      if (row && !seen.has(row.key)) { seen.add(row.key); buf.push(row); }
    };
    es.addEventListener('end', () => { flush(); setEnded(true); setLive(false); es.close(); });
    es.onerror = () => { setLive(false); };
    return () => { clearInterval(timer); es.close(); seen = new Set(); };
  }, [url]);
  return { rows, live, ended };
}

/** History replay + live follow for one deployment; ends itself when the deployment reaches a terminal state. */
export function useDeploymentLogs(id: string | undefined) {
  return useLogStream(id ? `${API_BASE}/deployments/${id}/logs` : null, (data) => {
    try { const l = JSON.parse(data) as DeploymentLogLine; return { key: l.seq, at: l.at, stream: l.stream, text: l.line }; } catch { return null; }
  });
}

/** Plain-text container log tail (the contract streams raw lines). */
export function useContainerLogs(resourceId: string, enabled: boolean) {
  return useLogStream(enabled ? `${API_BASE}/resources/${resourceId}/logs?tail=100` : null, (data, _id, n) => {
    const m = /^(\d{4}-\d\d-\d\dT[\d:.]+Z?)\s+(.*)$/.exec(data);
    return { key: n, at: m?.[1], stream: /WARN|ERROR/.test(data) ? 'stderr' : 'stdout', text: m ? m[2] : data };
  });
}
