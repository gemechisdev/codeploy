import { afterEach, describe, expect, it } from 'vitest';
import { boot, TestClient } from './helpers';
import { GENESIS } from '../db';

let c: TestClient;
afterEach(() => c?.server.dispose());

describe('audit chain', () => {
  it('verifies a clean chain end to end', async () => {
    c = await boot();
    const r = await c.post('/audit/verify');
    expect(r.status).toBe(200);
    expect(r.body.valid).toBe(true);
    expect(r.body.verified_rows).toBeGreaterThanOrEqual(8);
  });

  it('every row links to its predecessor with a real SHA-256 hash', async () => {
    c = await boot();
    const rows = c.server.db.audit;
    expect(rows[0].prev_hash).toBe(GENESIS);
    for (let i = 1; i < rows.length; i++) expect(rows[i].prev_hash).toBe(rows[i - 1].hash);
    expect(rows.every((r) => /^[0-9a-f]{64}$/.test(r.hash))).toBe(true);
  });

  it('detects a tampered row and reports the first invalid sequence', async () => {
    c = await boot();
    const { tamperAudit } = await import('../engine');
    const seq = tamperAudit(c.server.db)!;
    const r = await c.post('/audit/verify');
    expect(r.body.valid).toBe(false);
    expect(r.body.first_invalid_seq).toBe(seq);
    expect(r.body.verified_rows).toBe(seq - 1);
  });

  it('detects a deleted row (the chain breaks at the gap)', async () => {
    c = await boot();
    c.server.db.audit.splice(3, 1);
    const r = await c.post('/audit/verify');
    expect(r.body.valid).toBe(false);
  });

  it('mutating handlers append audit rows in the same request; the client cannot write audit', async () => {
    c = await boot();
    const before = c.server.db.audit.length;
    await c.post('/projects', { name: 'Audit me', slug: 'audit-me' });
    const after = c.server.db.audit;
    expect(after.length).toBe(before + 1);
    expect(after.at(-1)).toMatchObject({ action: 'project.created', actor_email: 'owner@example.com' });
    expect((await c.post('/audit', { action: 'x' })).status).toBe(405); // no write route exists
  });

  it('filters by actor/action and paginates with cursors', async () => {
    c = await boot();
    for (let i = 0; i < 6; i++) await c.post('/projects', { name: `P${i}`, slug: `p-${i}` });
    const f = await c.get('/audit?action=project.created&limit=4');
    expect(f.body.items).toHaveLength(4);
    expect(f.body.next_cursor).toBe('4');
    const f2 = await c.get(`/audit?action=project.created&limit=4&cursor=${f.body.next_cursor}`);
    expect(f2.body.items.length).toBe(2);
    expect(f2.body.next_cursor).toBeNull();
    expect((await c.get('/audit?actor=nobody@nowhere')).body.items).toHaveLength(0);
    const range = await c.get(`/audit?from=${encodeURIComponent(new Date(Date.now() + 60_000).toISOString())}`);
    expect(range.body.items).toHaveLength(0);
  });
});
