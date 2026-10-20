import { afterEach, describe, expect, it } from 'vitest';
import { boot, until, TestClient } from './helpers';
import { createMockEventSource } from '../install';

let c: TestClient;
afterEach(() => c?.server.dispose());

const open = (url: string) => {
  const ES = createMockEventSource(c.server) as unknown as new (u: string) => EventTarget & { close(): void; readyState: number; onerror: unknown };
  const es = new ES(url);
  const got: { type: string; data: string; id: string }[] = [];
  const types = ['message', 'end', 'deployment.updated', 'resource.updated', 'deployment.log', 'audit.appended', 'hardening.changed', 'metrics.sample'];
  for (const t of types) es.addEventListener(t, (e) => got.push({ type: t, data: (e as MessageEvent).data, id: (e as MessageEvent).lastEventId }));
  let errored = false;
  es.addEventListener('error', () => { errored = true; });
  return { es, got, get errored() { return errored; } };
};

describe('SSE streams', () => {
  it('/events requires a session', async () => {
    c = await boot({ login: false });
    const s = open('http://localhost/api/v1/events');
    await until(() => s.errored);
    expect(s.es.readyState).toBe(2);
  });

  it('/events pushes typed events with ids and resumes from last_event_id', async () => {
    c = await boot();
    const s = open('http://localhost/api/v1/events');
    await until(() => s.es.readyState === 1);
    await c.post('/projects', { name: 'Evt', slug: 'evt' });
    await until(() => s.got.some((g) => g.type === 'audit.appended'));
    const first = s.got.find((g) => g.type === 'audit.appended')!;
    expect(first.id).toMatch(/^\d+$/);
    expect(JSON.parse(first.data)).toMatchObject({ seq: expect.any(Number), server_now: expect.any(String) });
    s.es.close();
    await c.post('/projects', { name: 'Evt2', slug: 'evt2' });
    const r = open(`http://localhost/api/v1/events?last_event_id=${first.id}`);
    await until(() => r.got.length > 0);
    expect(r.got.every((g) => Number(g.id) > Number(first.id))).toBe(true);
    r.es.close();
  });

  it('deployment logs replay history, follow live lines and end at a terminal state', async () => {
    c = await boot();
    const d = await c.post(`/resources/${c.resource('job-worker').id}/deployments`, {});
    const s = open(`http://localhost/api/v1/deployments/${d.body.id}/logs`);
    await until(() => s.got.some((g) => g.type === 'end'), 4000);
    const lines = s.got.filter((g) => g.type === 'message').map((g) => JSON.parse(g.data));
    expect(lines.length).toBeGreaterThan(5);
    expect(lines.map((l) => l.seq)).toEqual([...lines.map((l) => l.seq)].sort((a, b) => a - b));
    const again = open(`http://localhost/api/v1/deployments/${d.body.id}/logs?after=${lines.at(-3).seq}`);
    await until(() => again.got.some((g) => g.type === 'end'));
    expect(again.got.filter((g) => g.type === 'message')).toHaveLength(2);
  });

  it('container logs stream plain text lines for a running resource', async () => {
    c = await boot();
    c.server.db.speed = 0.01;
    const s = open(`http://localhost/api/v1/resources/${c.resource('storefront').id}/logs?tail=5`);
    await until(() => s.got.filter((g) => g.type === 'message').length >= 6, 3000);
    expect(s.got[0].data).toMatch(/^\d{4}-\d\d-\d\dT/);
    s.es.close();
  });

  it('unknown stream paths and ids fail instead of hanging', async () => {
    c = await boot();
    const s = open('http://localhost/api/v1/deployments/nope/logs');
    await until(() => s.errored);
  });
});
