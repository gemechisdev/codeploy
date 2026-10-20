import type { components } from '@/api/schema';
import { appendAudit, type Db } from './db';
import { expirePendingChangesNow, simulateDelivery, tamperAudit } from './engine';
import { createMockServer, type MockServer } from './server';
import { randomHex, ulid } from './util';

type S = components['schemas'];
const API = '/api/v1';
const TERMINAL = ['succeeded', 'failed', 'cancelled', 'interrupted', 'rolled_back'];

function pathOf(url: string): { path: string; q: URLSearchParams } {
  const u = new URL(url, 'http://localhost');
  return { path: u.pathname.startsWith(API) ? u.pathname.slice(API.length) : u.pathname, q: u.searchParams };
}

// ------------------------------------------------------------------ EventSource
export function createMockEventSource(server: MockServer): typeof EventSource {
  const db = server.db;
  class MockEventSource extends EventTarget {
    static readonly CONNECTING = 0; static readonly OPEN = 1; static readonly CLOSED = 2;
    readonly CONNECTING = 0; readonly OPEN = 1; readonly CLOSED = 2;
    readonly url: string; readonly withCredentials: boolean;
    readyState = 0;
    onopen: ((e: Event) => void) | null = null;
    onmessage: ((e: MessageEvent) => void) | null = null;
    onerror: ((e: Event) => void) | null = null;
    private cleanups: (() => void)[] = [];

    constructor(url: string | URL, init?: EventSourceInit) {
      super();
      this.url = String(url);
      this.withCredentials = !!init?.withCredentials;
      const t = setTimeout(() => this.start(), 20);
      this.cleanups.push(() => clearTimeout(t));
    }
    private emit(type: string, data: string, id = '') {
      if (this.readyState === 2) return;
      const ev = new MessageEvent(type, { data, lastEventId: id });
      this.dispatchEvent(ev);
      if (type === 'message') this.onmessage?.(ev);
    }
    private fail() {
      this.close();
      const ev = new Event('error');
      this.dispatchEvent(ev);
      this.onerror?.(ev);
    }
    private open() {
      this.readyState = 1;
      const ev = new Event('open');
      this.dispatchEvent(ev);
      this.onopen?.(ev);
    }
    private start() {
      if (this.readyState === 2) return;
      const { path, q } = pathOf(this.url);
      const s = db.session;
      if (!s || s.pending_challenge || !db.users.some((u) => u.id === s.user_id && !u.disabled)) return this.fail();
      let m: RegExpExecArray | null;
      if (path === '/events') {
        this.open();
        const since = q.get('last_event_id');
        if (since) for (const e of db.bus.since(since)) this.emit(e.type, JSON.stringify({ ...e.data, server_now: new Date().toISOString() }), e.id);
        this.cleanups.push(db.bus.subscribe((e) => this.emit(e.type, JSON.stringify(e.type === 'metrics.sample' ? e.data : { ...e.data, server_now: new Date().toISOString() }), e.id)));
      } else if ((m = /^\/deployments\/([^/]+)\/logs$/.exec(path))) {
        const id = m[1];
        const d = db.deployments.find((x) => x.id === id);
        if (!d) return this.fail();
        this.open();
        const after = Number(q.get('after')) || 0;
        for (const l of db.logs.get(id) ?? []) if (l.seq > after) this.emit('message', JSON.stringify(l), String(l.seq));
        const finish = () => { this.emit('end', '{}'); setTimeout(() => this.close(), 0); };
        if (TERMINAL.includes(d.status)) return finish();
        this.cleanups.push(db.bus.subscribe((e) => {
          if (e.type === 'deployment.log' && e.data.deployment_id === id) this.emit('message', JSON.stringify(e.data), String(e.data.seq));
          if (e.type === 'deployment.updated' && e.data.deployment_id === id && TERMINAL.includes(String(e.data.status))) finish();
        }));
      } else if ((m = /^\/resources\/([^/]+)\/logs$/.exec(path))) {
        const r = db.resources.find((x) => x.id === m![1]);
        if (!r) return this.fail();
        this.open();
        const tail = Math.min(Number(q.get('tail')) || 40, 500);
        const line = (at: number) => {
          const kind = Math.random();
          const t = new Date(at).toISOString();
          if (kind < 0.7) return `${t} GET ${r.config.healthcheck.path} 200 ${Math.floor(Math.random() * 9) + 1}ms`;
          if (kind < 0.9) return `${t} GET /api/items?page=${Math.floor(Math.random() * 5)} 200 ${Math.floor(Math.random() * 60) + 8}ms`;
          return `${t} WARN slow query (${Math.floor(Math.random() * 400) + 200}ms) on ${r.slug}`;
        };
        for (let i = tail; i > 0; i--) this.emit('message', line(Date.now() - i * 4000));
        if (r.status === 'stopped') return setTimeout(() => this.emit('end', ''), 0), undefined;
        const iv = setInterval(() => this.emit('message', line(Date.now())), Math.max(250, 1500 * db.speed));
        this.cleanups.push(() => clearInterval(iv));
      } else this.fail();
    }
    close() {
      this.readyState = 2;
      this.cleanups.splice(0).forEach((c) => c());
    }
  }
  return MockEventSource as unknown as typeof EventSource;
}

// ------------------------------------------------------------------ WebSocket + fake PTY
const enc = new TextEncoder();
const dec = new TextDecoder();

class Shell {
  private buf = '';
  constructor(private o: { user: string; host: string; root: boolean; db: Db; container: boolean; send: (s: string) => void; exit: (code: number) => void }) {}
  get prompt() { return `${this.o.user}@${this.o.host}:~${this.o.root ? '#' : '$'} `; }
  start() { this.o.send(`Welcome to ${this.o.host} (mock shell: no real commands run)\r\nType "help" for the commands this demo understands.\r\n${this.prompt}`); }
  input(s: string) {
    for (let i = 0; i < s.length; i++) {
      const ch = s[i];
      if (ch === '\x1b') { i = s.length; break; } // ignore escape sequences (arrows etc.)
      if (ch === '\r' || ch === '\n') { this.o.send('\r\n'); this.run(this.buf.trim()); this.buf = ''; }
      else if (ch === '\x7f') { if (this.buf) { this.buf = this.buf.slice(0, -1); this.o.send('\b \b'); } }
      else if (ch === '\x03') { this.buf = ''; this.o.send(`^C\r\n${this.prompt}`); }
      else if (ch === '\x04') { if (!this.buf) this.o.exit(0); }
      else if (ch >= ' ') { this.buf += ch; this.o.send(ch); }
    }
  }
  private run(cmd: string) {
    const { db } = this.o;
    const [name, ...args] = cmd.split(/\s+/);
    const out: string[] = [];
    switch (name) {
      case '': break;
      case 'help': out.push('help whoami id hostname pwd ls uname uptime date df free docker echo clear exit'); break;
      case 'whoami': out.push(this.o.user); break;
      case 'id': out.push(this.o.root ? 'uid=0(root) gid=0(root) groups=0(root)' : `uid=1001(${this.o.user}) gid=1001(${this.o.user}) groups=1001(${this.o.user}),27(sudo),999(docker)`); break;
      case 'hostname': out.push(this.o.host); break;
      case 'pwd': out.push(this.o.root ? '/root' : `/home/${this.o.user}`); break;
      case 'ls': out.push(this.o.container ? 'app  node_modules  package.json' : 'bin  deploy  logs'); break;
      case 'uname': out.push('Linux codeplo-vps 6.1.0-26-amd64 #1 SMP Debian 6.1.112-1 x86_64 GNU/Linux'); break;
      case 'uptime': out.push(' 12:04:11 up 6 days,  3:12,  1 user,  load average: 0.08, 0.04, 0.01'); break;
      case 'date': out.push(new Date().toString()); break;
      case 'df': out.push('Filesystem  Size  Used Avail Use% Mounted on', `/dev/vda1    ${db.metrics.disk_total_gb}G  ${db.metrics.disk_used_gb}G  ${(db.metrics.disk_total_gb - db.metrics.disk_used_gb).toFixed(1)}G  ${Math.round((db.metrics.disk_used_gb / db.metrics.disk_total_gb) * 100)}% /`); break;
      case 'free': out.push('              total   used   free', `Mem:          ${db.metrics.memory_total_mb}    ${db.metrics.memory_used_mb}   ${db.metrics.memory_total_mb - db.metrics.memory_used_mb}`); break;
      case 'docker':
        if (args[0] === 'ps') { out.push('NAMES                          STATUS'); for (const r of db.resources.filter((x) => x.status === 'running')) out.push(`${`cpl-${r.slug}`.padEnd(30)} Up ${r.environment_name}`); }
        else out.push('docker: this demo shell only supports "docker ps"');
        break;
      case 'echo': out.push(args.join(' ')); break;
      case 'clear': this.o.send('\x1b[2J\x1b[H'); break;
      case 'exit': case 'logout': this.o.exit(0); return;
      case 'su': case 'sudo': out.push(`${name}: not available in the demo shell (root access is a separate, step-up protected session)`); break;
      default: out.push(`bash: ${name}: command not found`);
    }
    this.o.send(out.map((l) => `${l}\r\n`).join('') + this.prompt);
  }
}

export function createMockWebSocket(server: MockServer): typeof WebSocket {
  const db = server.db;
  class MockWebSocket extends EventTarget {
    static readonly CONNECTING = 0; static readonly OPEN = 1; static readonly CLOSING = 2; static readonly CLOSED = 3;
    readonly CONNECTING = 0; readonly OPEN = 1; readonly CLOSING = 2; readonly CLOSED = 3;
    readonly url: string; readonly protocol = ''; readonly extensions = ''; bufferedAmount = 0;
    binaryType: BinaryType = 'blob';
    readyState = 0;
    onopen: ((e: Event) => void) | null = null;
    onmessage: ((e: MessageEvent) => void) | null = null;
    onclose: ((e: Event) => void) | null = null;
    onerror: ((e: Event) => void) | null = null;
    private shell: Shell | null = null;
    private idle: ReturnType<typeof setTimeout> | null = null;
    private idleMs = 0;
    private session: S['TerminalSession'] | null = null;
    /** Test hook: last resize received. */
    lastResize: { cols: number; rows: number } | null = null;
    readonly received: string[] = [];

    constructor(url: string | URL) {
      super();
      this.url = String(url);
      setTimeout(() => this.start(), 15);
    }
    private fire(type: string, ev: Event, handler: ((e: never) => void) | null) {
      this.dispatchEvent(ev);
      (handler as ((e: Event) => void) | null)?.(ev);
      void type;
    }
    private reject(code: number, reason: string) {
      this.fire('error', new Event('error'), this.onerror);
      this.finish(code, reason);
    }
    private finish(code: number, reason: string) {
      if (this.readyState === 3) return;
      this.readyState = 3;
      if (this.idle) clearTimeout(this.idle);
      if (this.session && !this.session.ended_at) this.session.ended_at = new Date().toISOString();
      const ev = Object.assign(new Event('close'), { code, reason, wasClean: code === 1000 });
      this.fire('close', ev, this.onclose);
    }
    private text(s: string) { this.deliver(s); }
    private bytes(s: string) {
      const u = enc.encode(s);
      this.deliver(this.binaryType === 'arraybuffer' ? u.buffer.slice(u.byteOffset, u.byteOffset + u.byteLength) : new Blob([u]));
    }
    private deliver(data: string | ArrayBuffer | Blob) {
      if (this.readyState !== 1) return;
      const ev = new MessageEvent('message', { data });
      this.dispatchEvent(ev);
      this.onmessage?.(ev);
    }
    private bump() {
      if (this.idle) clearTimeout(this.idle);
      this.idle = setTimeout(() => {
        this.text(JSON.stringify({ type: 'error', message: 'Idle timeout: the session was closed' }));
        this.finish(1000, 'idle timeout');
      }, this.idleMs);
    }
    private start() {
      if (this.readyState !== 0) return;
      const { path, q } = pathOf(this.url);
      const s = db.session;
      if (!s || s.pending_challenge) return this.reject(1008, 'unauthenticated');
      const hostMatch = path === '/terminal/host';
      const resMatch = /^\/terminal\/resources\/([^/]+)$/.exec(path);
      if (!hostMatch && !resMatch) return this.reject(1008, 'unknown terminal route');
      const t = db.tickets.get(q.get('ticket') ?? '');
      if (!t || t.used || t.expires_at < Date.now() || t.user_id !== s.user_id) return this.reject(1008, 'invalid or expired ticket');
      if ((hostMatch && t.target !== 'host') || (resMatch && (t.target !== 'container' || t.resource_id !== resMatch[1]))) return this.reject(1008, 'ticket does not match this terminal');
      t.used = true; // single use
      const user = db.users.find((u) => u.id === s.user_id)!;
      const res = resMatch ? db.resources.find((r) => r.id === resMatch[1]) : null;
      this.readyState = 1;
      this.session = {
        id: ulid('trm_'), user_email: user.email, ip: '198.51.100.24', target: hostMatch ? 'host' : 'container', target_name: res?.name ?? 'codeplo-vps',
        as_user: t.as_root ? 'root' : hostMatch ? (db.operator ?? 'operator') : 'app', is_root: t.as_root, started_at: new Date().toISOString(), ended_at: null, recorded: false,
      };
      db.termSessions.unshift(this.session);
      void appendAudit(db, { actor_type: 'user', actor_id: user.id, actor_email: user.email, ip: '198.51.100.24', action: 'terminal.session_opened', target_type: 'terminal', target_id: this.session.id, meta: { target: this.session.target, as_root: t.as_root } });
      this.idleMs = Math.max(500, Number(db.settings['terminal.idle_timeout_s'] ?? 900) * 1000 * (db.speed === 0 ? 0.01 : 1));
      this.shell = new Shell({
        user: this.session.as_user, host: res ? `cpl-${res.slug}` : 'codeplo-vps', root: t.as_root, db, container: !!res,
        send: (x) => this.bytes(x), exit: (code) => { this.text(JSON.stringify({ type: 'exit', code })); this.finish(1000, 'exit'); },
      });
      const open = new Event('open');
      this.dispatchEvent(open);
      this.onopen?.(open);
      this.shell.start();
      this.bump();
    }
    send(data: string | ArrayBuffer | ArrayBufferView | Blob) {
      if (this.readyState !== 1) throw new DOMException('WebSocket is not open', 'InvalidStateError');
      this.bump();
      if (typeof data === 'string') {
        this.received.push(data);
        try {
          const m = JSON.parse(data) as { type?: string; cols?: number; rows?: number };
          if (m.type === 'resize' && m.cols && m.rows) this.lastResize = { cols: m.cols, rows: m.rows };
        } catch { /* ignore malformed control frames */ }
        return;
      }
      // Local echo is the PTY's job: this mock shell echoes, the UI never does.
      const bytes = data instanceof ArrayBuffer ? new Uint8Array(data) : ArrayBuffer.isView(data) ? new Uint8Array(data.buffer, data.byteOffset, data.byteLength) : null;
      if (bytes) this.shell?.input(dec.decode(bytes));
    }
    close(code = 1000, reason = '') { this.finish(code, reason); }
  }
  return MockWebSocket as unknown as typeof WebSocket;
}

// ------------------------------------------------------------------ install + dev hooks
export interface MockHooks {
  server: MockServer;
  setFault(f: Db['fault']): void;
  setSpeed(n: number): void;
  loginAs(email: string): string;
  expirePendingChangesNow(): void;
  tamperAudit(seq?: number): number | null;
  simulateDelivery(result?: 'success' | 'ignored' | 'rejected'): void;
  setScenario(s: Partial<Db['scenario']>): void;
}

export function buildHooks(server: MockServer): MockHooks {
  const db = server.db;
  return {
    server,
    setFault: (f) => { db.fault = f; },
    setSpeed: (n) => { db.speed = n; },
    loginAs(email) {
      const u = db.users.find((x) => x.email === email);
      if (!u) throw new Error(`no such mock user ${email}`);
      db.session = { user_id: u.id, csrf: randomHex(16), step_up_until: null, bootstrap: false };
      return db.session.csrf;
    },
    expirePendingChangesNow: () => expirePendingChangesNow(db),
    tamperAudit: (seq) => tamperAudit(db, seq),
    simulateDelivery: (r) => void simulateDelivery(db, r),
    setScenario: (s) => Object.assign(db.scenario, s),
  };
}

declare global {
  // eslint-disable-next-line no-var
  var __codeploMock: MockHooks | undefined;
}

export async function installMock(): Promise<MockHooks> {
  const params = new URLSearchParams(typeof location === 'undefined' ? '' : location.search);
  const server = await createMockServer({ fresh: params.get('mock') === 'fresh', speed: params.has('mock_speed') ? Number(params.get('mock_speed')) : 1 });
  const hooks = buildHooks(server);
  let fault = params.get('mock_fault');
  try { fault ??= localStorage.getItem('mock_fault'); } catch { /* storage unavailable */ }
  if (fault && ['500', '429', 'step_up', 'slow'].includes(fault)) server.db.fault = fault as Db['fault'];

  const realFetch = globalThis.fetch?.bind(globalThis);
  globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
    const raw = input instanceof Request ? input.url : String(input);
    const u = new URL(raw, typeof location === 'undefined' ? 'http://localhost' : location.href);
    if (u.pathname.startsWith(API)) return server.fetch(input instanceof Request ? input : new Request(u, init));
    return realFetch(input, init);
  }) as typeof fetch;
  globalThis.EventSource = createMockEventSource(server);
  globalThis.WebSocket = createMockWebSocket(server);
  globalThis.__codeploMock = hooks;
  return hooks;
}
