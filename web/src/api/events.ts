import type { QueryClient } from '@tanstack/react-query';
import { API_BASE } from './client';
import { qk } from './keys';
import { logBus } from './logbus';
import { syncServerNow } from './session';
import type { DeploymentLogLine, HostMetrics, Me } from './types';

export interface EventConnection {
  close(): void;
}

type Payload = Record<string, unknown>;

/** Maps each realtime event to the query keys it invalidates (see brief 5.4). */
export function applyEvent(qc: QueryClient, type: string, data: Payload): void {
  switch (type) {
    case 'resource.updated':
      void qc.invalidateQueries({ queryKey: qk.resource(String(data.resource_id)) });
      void qc.invalidateQueries({ queryKey: qk.projects });
      void qc.invalidateQueries({ queryKey: ['resources', 'list'] });
      break;
    case 'deployment.updated':
      void qc.invalidateQueries({ queryKey: qk.deployments(String(data.resource_id)) });
      void qc.invalidateQueries({ queryKey: qk.deployment(String(data.deployment_id)) });
      void qc.invalidateQueries({ queryKey: qk.resource(String(data.resource_id)) });
      void qc.invalidateQueries({ queryKey: qk.projects });
      break;
    case 'deployment.log':
      logBus.publish(String(data.deployment_id), data as unknown as DeploymentLogLine);
      break;
    case 'hardening.changed':
      void qc.invalidateQueries({ queryKey: ['hardening'] });
      void qc.invalidateQueries({ queryKey: qk.system.status });
      break;
    case 'edge.changed':
      void qc.invalidateQueries({ queryKey: ['edge'] });
      void qc.invalidateQueries({ queryKey: ['resources'] });
      break;
    case 'audit.appended':
      void qc.invalidateQueries({ queryKey: ['audit'] });
      break;
    case 'metrics.sample': {
      const m = data as unknown as HostMetrics;
      qc.setQueryData(['system', 'live-metrics'], m);
      break;
    }
    case 'session.step_up':
      qc.setQueryData<Me | undefined>(qk.me, (me) => (me ? { ...me, step_up_until: String(data.step_up_until) } : me));
      break;
    case 'module.changed':
      void qc.invalidateQueries({ queryKey: qk.modules });
      break;
    case 'flag.changed':
      void qc.invalidateQueries({ queryKey: qk.flags });
      void qc.invalidateQueries({ queryKey: qk.settings });
      void qc.invalidateQueries({ queryKey: qk.hardening.checks });
      break;
  }
}

export const EVENT_TYPES = [
  'resource.updated', 'deployment.updated', 'deployment.log', 'hardening.changed', 'edge.changed',
  'audit.appended', 'metrics.sample', 'session.step_up', 'module.changed', 'flag.changed',
] as const;

export interface EventsOptions {
  /** Injected for tests; defaults to the global EventSource. */
  eventSource?: typeof EventSource;
  backoffMs?: (attempt: number) => number;
}

/**
 * One app-wide SSE connection. Reconnects with exponential backoff and resumes from the last
 * event id via `?last_event_id=` (EventSource cannot set headers once recreated).
 */
export function connectEvents(qc: QueryClient, opts: EventsOptions = {}): EventConnection {
  const ES = opts.eventSource ?? globalThis.EventSource;
  const backoff = opts.backoffMs ?? ((n) => Math.min(30_000, 500 * 2 ** n));
  let es: EventSource | null = null;
  let attempt = 0;
  let lastId = '';
  let timer: ReturnType<typeof setTimeout> | null = null;
  let closed = false;

  const open = () => {
    if (closed) return;
    const url = `${API_BASE}/events${lastId ? `?last_event_id=${encodeURIComponent(lastId)}` : ''}`;
    es = new ES(url, { withCredentials: true });
    es.onopen = () => {
      if (attempt > 0) {
        // We may have missed events while disconnected: refresh everything once.
        void qc.invalidateQueries();
      }
      attempt = 0;
    };
    for (const t of EVENT_TYPES) {
      es.addEventListener(t, (ev) => {
        const m = ev as MessageEvent<string>;
        if (m.lastEventId) lastId = m.lastEventId;
        let data: Payload = {};
        try {
          data = JSON.parse(m.data) as Payload;
        } catch {
          return;
        }
        if (typeof data.server_now === 'string') syncServerNow(data.server_now);
        applyEvent(qc, t, data);
      });
    }
    es.onerror = () => {
      es?.close();
      es = null;
      if (closed) return;
      timer = setTimeout(open, backoff(attempt++));
    };
  };
  open();

  return {
    close() {
      closed = true;
      if (timer) clearTimeout(timer);
      es?.close();
    },
  };
}
