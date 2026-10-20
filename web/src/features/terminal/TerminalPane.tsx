import { Plug, PlugZap, TriangleAlert } from 'lucide-react';
import { useCallback, useEffect, useRef, useState } from 'react';
import { API_BASE } from '@/api/client';
import { useTerminalTicket } from '@/api/hooks/workloads';
import { ApiError, errorMessage } from '@/api/errors';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';

type State = 'idle' | 'connecting' | 'connected' | 'closed';
interface Props { target: 'host' | 'container'; resourceId?: string; asRoot?: boolean; label: string }

const wsUrl = (path: string, ticket: string) => `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}${API_BASE}${path}?ticket=${encodeURIComponent(ticket)}`;

/**
 * xterm.js over a ticketed WebSocket. Ticket = single-use, 30 s, issued via an authenticated request (step-up for root).
 * Input goes out as bytes, resize as a JSON control frame. There is NO local echo: the PTY on the other side echoes.
 */
export function TerminalPane({ target, resourceId, asRoot = false, label }: Props) {
  const host = useRef<HTMLDivElement>(null);
  const ws = useRef<WebSocket | null>(null);
  const dispose = useRef<(() => void) | null>(null);
  const [state, setState] = useState<State>('idle');
  const [note, setNote] = useState<string | null>(null);
  const ticket = useTerminalTicket();

  const teardown = useCallback(() => { ws.current?.close(); ws.current = null; dispose.current?.(); dispose.current = null; }, []);
  useEffect(() => teardown, [teardown]);

  const connect = async () => {
    setNote(null); teardown(); setState('connecting');
    let t: { ticket: string };
    try { t = await ticket.mutateAsync({ target, resource_id: resourceId, as_root: asRoot || undefined }); }
    catch (e) {
      setState('idle');
      setNote(e instanceof ApiError && e.details && (e.details as { reason?: string }).reason === 'operator_missing' ? 'This server has no operator identity yet. Configure one under Security first.' : errorMessage(e));
      return;
    }
    const [{ Terminal }, { FitAddon }] = await Promise.all([import('@xterm/xterm'), import('@xterm/addon-fit'), import('@xterm/xterm/css/xterm.css')]);
    if (!host.current) return;
    const term = new Terminal({ cursorBlink: true, fontFamily: "'JetBrains Mono Variable','JetBrains Mono',ui-monospace,monospace", fontSize: 13, scrollback: 3000, theme: { background: '#0c0513', foreground: '#e8dcf2', cursor: '#62D6C3', selectionBackground: '#A43CC766' } });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(host.current);
    const send = (m: unknown) => { if (ws.current?.readyState === 1) ws.current.send(JSON.stringify(m)); };
    const doFit = () => { try { fit.fit(); send({ type: 'resize', cols: term.cols, rows: term.rows }); } catch { /* hidden tab: fit later */ } };

    const sock = new WebSocket(wsUrl(target === 'host' ? '/terminal/host' : `/terminal/resources/${resourceId}`, t.ticket));
    sock.binaryType = 'arraybuffer';
    ws.current = sock;
    const enc = new TextEncoder();
    const dataSub = term.onData((d) => { if (sock.readyState === 1) sock.send(enc.encode(d)); });
    let timer: ReturnType<typeof setTimeout> | undefined;
    const ro = new ResizeObserver(() => { clearTimeout(timer); timer = setTimeout(doFit, 120); });
    ro.observe(host.current);
    sock.onopen = () => { setState('connected'); doFit(); term.focus(); };
    sock.onmessage = (e) => {
      if (typeof e.data === 'string') {
        try {
          const m = JSON.parse(e.data) as { type?: string; message?: string; code?: number };
          if (m.type === 'error') { term.write(`\r\n\x1b[31m${m.message ?? 'Session error'}\x1b[0m\r\n`); setNote(m.message ?? 'Session error'); }
          if (m.type === 'exit') term.write(`\r\n\x1b[2m[process exited with code ${m.code ?? 0}]\x1b[0m\r\n`);
        } catch { /* ignore malformed control frame */ }
      } else term.write(new Uint8Array(e.data as ArrayBuffer));
    };
    sock.onclose = (e) => { setState('closed'); if (e.code === 1008) setNote(`Connection refused: ${e.reason || 'the ticket was invalid or expired'}. Press Connect to get a new one.`); };
    dispose.current = () => { clearTimeout(timer); ro.disconnect(); dataSub.dispose(); term.dispose(); };
  };

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <Badge tone={state === 'connected' ? 'green' : state === 'connecting' ? 'brand' : 'grey'}>{state === 'idle' ? 'Not connected' : state}</Badge>
        {asRoot && <Badge tone="red"><TriangleAlert aria-hidden className="size-3" />root</Badge>}
        <span className="min-w-0 flex-1 truncate text-xs text-muted">{label}</span>
        {state === 'connected'
          ? <Button size="sm" variant="secondary" onClick={() => { teardown(); setState('closed'); }}><Plug className="size-3.5" />Disconnect</Button>
          : <Button size="sm" loading={state === 'connecting'} onClick={() => void connect()}><PlugZap className="size-3.5" />{state === 'closed' ? 'Reconnect' : 'Connect'}</Button>}
      </div>
      {note && <p role="alert" className="rounded-lg bg-amber-500/10 p-2.5 text-xs text-amber-800 dark:text-amber-300">{note}</p>}
      <div className="relative overflow-hidden rounded-xl border border-subtle bg-code-bg">
        <div ref={host} role="application" aria-label={`Terminal for ${label}`} className="h-96 p-2" />
        {state === 'idle' && <p className="pointer-events-none absolute inset-0 flex items-center justify-center text-xs text-zinc-500">Press Connect to open a shell.</p>}
      </div>
      <p className="text-[11px] text-muted">Sessions end after the idle timeout set by an admin. Every session is recorded in the session history.</p>
    </div>
  );
}
