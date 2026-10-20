import { ALL_PERMISSIONS, PERMISSION_DOCS, ROLE_PERMISSIONS, recomputeHash, GENESIS, type MockUser, type RoleKey } from '../db';
import { audit, need, noContent, ok, paginate, requireStepUp, route, permsOf, type Ctx } from '../router';
import { HttpError, hostRe, invalid, randomHex, ulid } from '../util';
import { applyDomainCreate, refreshOperatorCheck } from '../engine';

export const DEMO_TOTP = '123456';
const failed = new Map<string, { n: number; until: number }>();

export const publicUser = (u: MockUser) => ({ id: u.id, email: u.email, role: u.role, totp_enabled: u.totp_enabled, disabled: u.disabled, created_at: u.created_at, last_login_at: u.last_login_at });
const meBody = (c: Ctx) => {
  const u = c.user!; const s = c.session!;
  return {
    user: publicUser(u), role: u.role, permissions: permsOf(u),
    step_up_until: s.step_up_until && s.step_up_until > Date.now() ? new Date(s.step_up_until).toISOString() : null,
    csrf_token: s.csrf, server_now: new Date().toISOString(), bootstrap: s.bootstrap,
  };
};

function requireSetup(c: Ctx) {
  if (!c.db.setup.required) throw new HttpError(404, 'not_found', 'Setup is disabled: an owner already exists');
}

export function registerAuth() {
  // ---------- setup (public; works once, only while no owner exists) ----------
  route('GET', '/setup/state', null, (c) => {
    const s = c.db.setup;
    return ok({ setup_required: s.required, token_valid: s.admin_id !== null, step: s.step, has_operator: !!c.db.operator });
  });
  route('POST', '/setup/admin', null, (c) => {
    requireSetup(c);
    const b = c.body ?? {};
    if (b.token !== c.db.setup.token) throw new HttpError(403, 'forbidden', 'The one-time setup token is invalid or expired');
    const fields: Record<string, string> = {};
    if (typeof b.email !== 'string' || !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(b.email)) fields.email = 'Enter a valid email address';
    if (typeof b.password !== 'string' || b.password.length < 12) fields.password = 'Use at least 12 characters';
    if (Object.keys(fields).length) throw invalid(fields);
    if (c.db.setup.admin_id) throw new HttpError(409, 'conflict', 'Owner already created');
    const now = new Date().toISOString();
    const u: MockUser = { id: ulid('usr_'), email: b.email, password: b.password, role: 'owner', totp_enabled: false, disabled: false, created_at: now, last_login_at: null };
    c.db.users.push(u);
    c.db.setup.admin_id = u.id;
    c.db.setup.step = 'totp';
    void audit({ ...c, user: u }, 'setup.admin_created', 'user', u.id, {});
    return ok(publicUser(u), 201);
  });
  route('POST', '/setup/totp/enroll', null, (c) => {
    requireSetup(c);
    const u = c.db.users.find((x) => x.id === c.db.setup.admin_id);
    if (!u) throw new HttpError(409, 'conflict', 'Create the owner first');
    const B32 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
    const rnd = new Uint8Array(16);
    crypto.getRandomValues(rnd);
    const secret = [...rnd].map((b) => B32[b % 32]).join(''); // valid RFC 4648 base32, as authenticator apps require
    c.db.setup.totp_secret = secret;
    return ok({ secret, otpauth_uri: `otpauth://totp/codeplo:${encodeURIComponent(u.email)}?secret=${secret}&issuer=codeplo&algorithm=SHA1&digits=6&period=30` });
  });
  route('POST', '/setup/totp/verify', null, (c) => {
    requireSetup(c);
    const u = c.db.users.find((x) => x.id === c.db.setup.admin_id);
    if (!u || !c.db.setup.totp_secret) throw new HttpError(409, 'conflict', 'Start TOTP enrollment first');
    if (c.body?.code !== DEMO_TOTP) throw invalid({ code: 'That code is not valid. Check your authenticator and try again.' });
    u.totp_enabled = true;
    c.db.setup.totp_verified = true;
    c.db.setup.recovery_issued = true;
    c.db.setup.step = 'domain';
    void audit({ ...c, user: u }, 'setup.totp_enrolled', 'user', u.id, {});
    const recovery_codes = Array.from({ length: 8 }, () => `${randomHex(2)}-${randomHex(2)}-${randomHex(2)}`);
    return ok({ recovery_codes });
  });
  route('POST', '/setup/verify-domain', null, (c) => {
    requireSetup(c);
    const h = String(c.body?.hostname ?? '').toLowerCase();
    if (!hostRe.test(h)) throw invalid({ hostname: 'Enter a valid hostname such as panel.example.com' });
    const host_ips = ['203.0.113.10', '2001:db8::10'];
    if (h.includes('nodns')) return ok({ hostname: h, resolves: false, resolved_ips: [], host_ips, matches_host: false, detail: 'No A/AAAA record found yet. DNS changes can take a while to propagate.' });
    if (h.includes('wrong')) return ok({ hostname: h, resolves: true, resolved_ips: ['192.0.2.55'], host_ips, matches_host: false, detail: 'The name resolves to 192.0.2.55, which is not this host.' });
    return ok({ hostname: h, resolves: true, resolved_ips: ['203.0.113.10'], host_ips, matches_host: true, detail: 'Resolves to this host.' });
  });
  route('POST', '/setup/panel-domain', null, (c) => {
    requireSetup(c);
    const h = String(c.body?.hostname ?? '').toLowerCase();
    if (!hostRe.test(h)) throw invalid({ hostname: 'Enter a valid hostname such as panel.example.com' });
    let d = c.db.domains.find((x) => x.hostname === h);
    if (!d) d = applyDomainCreate(c.db, { hostname: h, resource: null, force_https: true, redirect_www: false });
    if (d.cert_status === 'issued') c.db.setup.panel_domain = h;
    return ok({ hostname: h, cert_status: d.cert_status, url: d.cert_status === 'issued' ? `https://${h}` : null, detail: d.cert_status === 'error' ? (d.cert_error ?? 'Issuance failed') : d.cert_status === 'issuing' ? 'Waiting for the certificate. Nothing is switched until issuance succeeds.' : 'Certificate issued. The SSH tunnel path keeps working.' });
  });
  route('GET', '/setup/operator-candidates', null, (c) => {
    requireSetup(c);
    return ok({ items: [{ username: 'ubuntu', uid: 1000, sudo: true, has_authorized_keys: true }, { username: 'ops-deployer', uid: 1001, sudo: true, has_authorized_keys: true }, { username: 'backup', uid: 1002, sudo: false, has_authorized_keys: false }] });
  });
  route('POST', '/setup/operator', null, (c) => {
    requireSetup(c);
    const b = c.body ?? {};
    const name = String(b.username ?? '');
    if (b.mode === 'create') {
      const f: Record<string, string> = {};
      if (!/^[a-z_][a-z0-9_-]{0,31}$/.test(name)) f.username = 'Lowercase letters, digits, "-" and "_" (max 32)';
      if (!/^ssh-(ed25519|rsa) /.test(String(b.public_key ?? ''))) f.public_key = 'Paste an SSH public key (ssh-ed25519 …)';
      if (Object.keys(f).length) throw invalid(f);
    } else if (!['ubuntu', 'ops-deployer'].includes(name)) throw invalid({ username: 'Choose a user with sudo access and an authorized_keys file' });
    c.db.operator = name;
    refreshOperatorCheck(c.db);
    c.db.setup.step = 'audit';
    return ok({ username: name, uid: 1001, sudo: true, has_authorized_keys: true });
  });
  route('POST', '/setup/complete', null, (c) => {
    requireSetup(c);
    const u = c.db.users.find((x) => x.id === c.db.setup.admin_id);
    if (!u || !u.totp_enabled) throw new HttpError(409, 'conflict', 'The owner must enroll TOTP before setup can complete');
    c.db.setup.required = false;
    c.db.setup.step = 'done';
    c.db.session = { user_id: u.id, csrf: randomHex(16), step_up_until: null, bootstrap: c.db.scenario.bootstrap };
    u.last_login_at = new Date().toISOString();
    void audit({ ...c, user: u }, 'setup.completed', 'system', 'setup', {});
    return noContent();
  });

  // ---------- auth ----------
  route('POST', '/auth/login', null, async (c) => {
    const email = String(c.body?.email ?? '').toLowerCase();
    const rec = failed.get(email);
    if (rec && rec.until > Date.now()) throw new HttpError(429, 'rate_limited', 'Too many failed attempts. Try again shortly.', undefined, { 'Retry-After': String(Math.ceil((rec.until - Date.now()) / 1000)) });
    const u = c.db.users.find((x) => x.email.toLowerCase() === email);
    if (!u || u.disabled || u.password !== c.body?.password) {
      const n = (rec?.n ?? 0) + 1;
      failed.set(email, { n, until: n >= 5 ? Date.now() + 30_000 : 0 });
      await audit({ ...c, user: null }, 'auth.login_failed', 'user', u?.id ?? 'unknown', {});
      throw new HttpError(401, 'unauthenticated', 'Invalid email or password');
    }
    failed.delete(email);
    const challenge = ulid('chl_');
    c.db.session = { user_id: u.id, csrf: randomHex(16), step_up_until: null, bootstrap: c.db.scenario.bootstrap, pending_challenge: challenge };
    return ok({ totp_required: true, challenge });
  });
  route('POST', '/auth/totp/verify', null, async (c) => {
    const s = c.db.session;
    if (!s || !s.pending_challenge || s.pending_challenge !== c.body?.challenge) throw new HttpError(401, 'unauthenticated', 'Login challenge expired. Sign in again.');
    if (c.body?.code !== DEMO_TOTP) throw invalid({ code: 'That code is not valid.' });
    const u = c.db.users.find((x) => x.id === s.user_id)!;
    s.pending_challenge = undefined;
    u.last_login_at = new Date().toISOString();
    await audit({ ...c, user: u }, 'auth.login', 'user', u.id, {});
    return ok(meBody({ ...c, user: u, session: s }));
  });
  route('GET', '/auth/me', null, (c) => {
    if (!c.user || !c.session || c.session.pending_challenge) throw new HttpError(401, 'unauthenticated', 'Not signed in');
    return ok(meBody(c));
  });
  route('POST', '/auth/step-up', 'session', async (c) => {
    if (c.body?.code !== DEMO_TOTP) throw invalid({ code: 'That code is not valid.' });
    const until = Date.now() + Number(c.db.settings.step_up_window_s ?? 300) * 1000;
    c.session!.step_up_until = until;
    const iso = new Date(until).toISOString();
    c.db.bus.emit('session.step_up', { step_up_until: iso });
    await audit(c, 'auth.step_up', 'user', c.user!.id, {});
    return ok({ step_up_until: iso });
  });
  route('POST', '/auth/logout', 'session', async (c) => {
    await audit(c, 'auth.logout', 'user', c.user!.id, {});
    c.db.session = null;
    return noContent();
  });

  // ---------- users / roles / permissions / tokens ----------
  route('GET', '/users', 'users:manage', (c) => ok(paginate(c.db.users.map(publicUser), c.query)));
  route('POST', '/users', 'users:manage', async (c) => {
    requireStepUp(c);
    const b = c.body ?? {};
    const f: Record<string, string> = {};
    if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(String(b.email ?? ''))) f.email = 'Enter a valid email address';
    if (String(b.password ?? '').length < 12) f.password = 'Use at least 12 characters';
    if (!['admin', 'deployer', 'viewer'].includes(b.role)) f.role = 'Choose admin, deployer or viewer';
    if (Object.keys(f).length) throw invalid(f);
    if (c.db.users.some((u) => u.email.toLowerCase() === String(b.email).toLowerCase())) throw new HttpError(409, 'conflict', 'A user with this email already exists');
    const u: MockUser = { id: ulid('usr_'), email: b.email, password: b.password, role: b.role as RoleKey, totp_enabled: false, disabled: false, created_at: new Date().toISOString(), last_login_at: null };
    c.db.users.push(u);
    await audit(c, 'user.created', 'user', u.id, { role: u.role });
    return ok(publicUser(u), 201);
  });
  route('PATCH', '/users/:id', 'users:manage', async (c) => {
    requireStepUp(c);
    const u = c.db.users.find((x) => x.id === c.params.id);
    if (!u) throw new HttpError(404, 'not_found', 'user not found');
    if (u.role === 'owner' && c.user!.role !== 'owner') throw new HttpError(403, 'forbidden', 'Only an owner can change an owner');
    if (u.id === c.user!.id && c.body?.disabled) throw new HttpError(409, 'conflict', 'You cannot disable your own account');
    if (c.body?.role && u.role === 'owner') throw new HttpError(409, 'conflict', 'The owner role cannot be changed');
    if (c.body?.role) u.role = c.body.role;
    if (typeof c.body?.disabled === 'boolean') u.disabled = c.body.disabled;
    await audit(c, 'user.updated', 'user', u.id, { role: u.role, disabled: u.disabled });
    return ok(publicUser(u));
  });
  route('GET', '/roles', 'session', () => ok({ items: (Object.keys(ROLE_PERMISSIONS) as RoleKey[]).map((key) => ({ key, name: key[0].toUpperCase() + key.slice(1), builtin: true, permissions: ROLE_PERMISSIONS[key] })) }));
  route('GET', '/permissions', 'session', () => ok({ items: ALL_PERMISSIONS.map((key) => ({ key, description: PERMISSION_DOCS[key] ?? key })) }));
  route('GET', '/tokens', 'tokens:manage', (c) => ok(paginate(c.db.tokens, c.query)));
  route('POST', '/tokens', 'tokens:manage', async (c) => {
    const b = c.body ?? {};
    const f: Record<string, string> = {};
    if (!String(b.name ?? '').trim()) f.name = 'Give the token a name';
    if (!Array.isArray(b.scopes) || !b.scopes.length) f.scopes = 'Select at least one scope';
    else if (b.scopes.some((s: string) => !(ALL_PERMISSIONS as string[]).includes(s))) f.scopes = 'Unknown scope';
    else if (b.scopes.some((s: string) => !permsOf(c.user!).includes(s as never))) f.scopes = 'You cannot grant scopes you do not hold';
    if (Object.keys(f).length) throw invalid(f);
    const secret = `cpl_${randomHex(20)}`;
    const token = { id: ulid('tok_'), name: String(b.name).trim(), prefix: secret.slice(0, 8), scopes: b.scopes, last_used_at: null, expires_at: b.expires_at ?? null, created_at: new Date().toISOString() };
    c.db.tokens.push(token);
    await audit(c, 'token.created', 'token', token.id, { scopes: token.scopes });
    return ok({ token, secret }, 201);
  });
  route('DELETE', '/tokens/:id', 'tokens:manage', async (c) => {
    const i = c.db.tokens.findIndex((t) => t.id === c.params.id);
    if (i < 0) throw new HttpError(404, 'not_found', 'token not found');
    c.db.tokens.splice(i, 1);
    await audit(c, 'token.revoked', 'token', c.params.id, {});
    return noContent();
  });

  // ---------- audit ----------
  route('GET', '/audit', 'audit:read', (c) => {
    const q = c.query;
    const actor = q.get('actor')?.toLowerCase(); const action = q.get('action')?.toLowerCase();
    const from = q.get('from') ? Date.parse(q.get('from')!) : null; const to = q.get('to') ? Date.parse(q.get('to')!) : null;
    const rows = [...c.db.audit].reverse().filter((r) => {
      if (actor && !(r.actor_email ?? r.actor_id).toLowerCase().includes(actor)) return false;
      if (action && !r.action.toLowerCase().includes(action)) return false;
      const at = Date.parse(r.at);
      return !((from && at < from) || (to && at > to));
    });
    return ok(paginate(rows, q));
  });
  route('POST', '/audit/verify', 'audit:read', async (c) => {
    let prev = GENESIS; let n = 0; let bad: number | null = null;
    for (const r of c.db.audit) {
      const expected = await recomputeHash(r, prev);
      if (r.prev_hash !== prev || r.hash !== expected) { bad = r.seq; break; }
      prev = r.hash; n++;
    }
    await audit(c, 'audit.verify', 'audit', 'chain', { valid: bad === null, verified_rows: n });
    return ok(bad === null ? { valid: true, verified_rows: n } : { valid: false, verified_rows: n, first_invalid_seq: bad });
  });
  void need;
}
