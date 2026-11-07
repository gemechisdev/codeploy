import { useQueryClient } from '@tanstack/react-query';
import { useNavigate, useSearch } from '@tanstack/react-router';
import { Check } from 'lucide-react';
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react';
import { meQueryOptions } from '@/api/hooks/auth';
import { useHardeningChecks, useRunAudit } from '@/api/hooks/hardening';
import { useCompleteSetup, useCreateAdmin, useEnrollTotp, useOperatorCandidates, useSetOperator, useSetPanelDomain, useSetupState, useVerifyDomain, useVerifySetupTotp } from '@/api/hooks/setup';
import { ApiError, errorMessage } from '@/api/errors';
import { CopyButton } from '@/components/app/CopyButton';
import { DeadManBanner } from '@/components/app/DeadManBanner';
import { QueryBoundary } from '@/components/app/QueryBoundary';
import { StatusBadge } from '@/components/app/StatusBadge';
import { Button } from '@/components/ui/button';
import { Card, CardBody } from '@/components/ui/card';
import { Field, Input, Select, Textarea } from '@/components/ui/input';
import { cn } from '@/lib/cn';
import { CheckCard } from '@/features/security/CheckCard';
import { QrCode } from './QrCode';

const STEPS = ['Owner', 'Two-factor', 'Domain', 'Operator', 'Harden'] as const;
type Step = 0 | 1 | 2 | 3 | 4;
const fromServer = (s?: string): Step => (s === 'totp' ? 1 : s === 'domain' ? 2 : s === 'operator' ? 3 : s === 'audit' || s === 'done' ? 4 : 0);

function Shell({ step, title, children }: { step: Step; title: string; children: ReactNode }) {
  return (
    <div className="flex min-h-screen items-start justify-center bg-canvas p-4 pt-10 sm:pt-16">
      <div className="w-full max-w-xl space-y-4">
        <div className="text-center"><span className="mx-auto mb-2 flex size-11 items-center justify-center rounded-xl bg-gradient-to-br from-brand-primary to-brand-secondary font-mono text-sm font-bold text-white">c/p</span><h1 className="text-xl font-bold tracking-tight text-primary">Set up codeplo</h1><p className="text-xs text-muted">First run on this server. Takes about five minutes.</p></div>
        <ol className="flex items-center justify-center gap-1.5" aria-label="Setup progress">
          {STEPS.map((s, i) => (
            <li key={s} aria-current={i === step ? 'step' : undefined} className={cn('flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[11px] font-semibold', i === step ? 'bg-brand-primary text-white' : i < step ? 'bg-emerald-500/10 text-emerald-700 dark:text-emerald-400' : 'bg-surface-muted text-muted')}>
              {i < step ? <Check aria-hidden className="size-3" /> : <span aria-hidden>{i + 1}</span>}<span className="hidden sm:inline">{s}</span><span className="sr-only sm:hidden">{s}</span>
            </li>
          ))}
        </ol>
        <Card><CardBody className="space-y-4"><h2 className="text-sm font-bold text-primary">{title}</h2>{children}</CardBody></Card>
      </div>
    </div>
  );
}

function OwnerStep({ initialToken, onDone }: { initialToken?: string; onDone: () => void }) {
  const m = useCreateAdmin();
  const [token, setToken] = useState(initialToken ?? ''); const [email, setEmail] = useState(''); const [pw, setPw] = useState(''); const [pw2, setPw2] = useState('');
  const err = m.error instanceof ApiError ? m.error : null; const mismatch = pw2 !== '' && pw !== pw2;
  const submit = (e: FormEvent) => { e.preventDefault(); if (!mismatch) m.mutate({ token, email: email.trim(), password: pw }, { onSuccess: onDone }); };
  return (
    <form onSubmit={submit} className="space-y-3" noValidate>
      <p className="text-xs text-secondary">Create the owner account. The setup token comes from the one-time URL printed by the installer.</p>
      <Field label="Setup token" error={err?.code === 'forbidden' ? err.message : err?.fields.token} hint={initialToken ? 'Taken from the setup URL.' : 'Open the setup URL from the installer, or paste the token.'}>{(a) => <Input {...a} className="font-mono" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} />}</Field>
      <Field label="Email" error={err?.fields.email}>{(a) => <Input {...a} type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} />}</Field>
      <Field label="Password" hint="At least 12 characters." error={err?.fields.password}>{(a) => <Input {...a} type="password" autoComplete="new-password" value={pw} onChange={(e) => setPw(e.target.value)} />}</Field>
      <Field label="Confirm password" error={mismatch ? 'Passwords do not match' : undefined}>{(a) => <Input {...a} type="password" autoComplete="new-password" value={pw2} onChange={(e) => setPw2(e.target.value)} />}</Field>
      {err && !['validation_failed', 'forbidden'].includes(err.code) && <p role="alert" className="text-xs text-red-600">{err.message}</p>}
      <div className="flex justify-end"><Button type="submit" loading={m.isPending} disabled={!token || !email || !pw || !pw2 || mismatch}>Create owner</Button></div>
    </form>
  );
}

function TotpStep({ onDone }: { onDone: () => void }) {
  const enroll = useEnrollTotp(); const verify = useVerifySetupTotp();
  const [code, setCode] = useState(''); const [codes, setCodes] = useState<string[] | null>(null); const [saved, setSaved] = useState(false);
  const started = useRef(false); const { mutate } = enroll;
  useEffect(() => { if (!started.current) { started.current = true; mutate(); } }, [mutate]);
  const err = verify.error instanceof ApiError ? verify.error : null;
  if (codes) return (
    <div className="space-y-3">
      <p role="alert" className="rounded-lg bg-amber-500/10 p-2.5 text-xs text-amber-800 dark:text-amber-300"><strong>Save these recovery codes now.</strong> Each works once if you lose your authenticator. They are not shown again.</p>
      <ul className="grid grid-cols-2 gap-1.5 rounded-lg bg-code-bg p-3 font-mono text-xs text-zinc-200">{codes.map((c) => <li key={c}>{c}</li>)}</ul>
      <div className="flex justify-end"><CopyButton value={codes.join('\n')} label="Copy all" /></div>
      <label className="flex cursor-pointer items-center gap-2 text-xs text-primary"><input type="checkbox" className="size-4 accent-brand-primary" checked={saved} onChange={(e) => setSaved(e.target.checked)} />I have stored my recovery codes somewhere safe</label>
      <div className="flex justify-end"><Button disabled={!saved} onClick={onDone}>Continue</Button></div>
    </div>
  );
  return (
    <form className="space-y-3" noValidate onSubmit={(e) => { e.preventDefault(); verify.mutate(code, { onSuccess: (r) => setCodes(r.recovery_codes) }); }}>
      <p className="text-xs text-secondary">Scan the QR code with an authenticator app, then enter the 6-digit code it shows.</p>
      <QueryBoundary query={{ data: enroll.data, isPending: enroll.isPending || enroll.isIdle, isError: enroll.isError, error: enroll.error, refetch: async () => { enroll.mutate(); return {} as never; } }} rows={2}>
        {(d) => (
          <div className="flex flex-col items-center gap-3 sm:flex-row sm:items-start">
            <QrCode value={d.otpauth_uri} label="Authenticator QR code" />
            <div className="min-w-0 space-y-1"><p className="text-[11px] text-muted">Can’t scan? Enter this key manually:</p><div className="flex items-center gap-2"><code className="break-all rounded bg-surface-muted px-2 py-1 font-mono text-xs text-primary">{d.secret}</code><CopyButton value={d.secret} iconOnly label="Copy key" /></div></div>
          </div>)}
      </QueryBoundary>
      <Field label="6-digit code" error={err?.fields.code ?? (err ? err.message : undefined)}>{(a) => <Input {...a} inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))} className="font-mono tracking-[0.4em]" />}</Field>
      <div className="flex justify-end"><Button type="submit" loading={verify.isPending} disabled={code.length !== 6 || !enroll.data}>Verify</Button></div>
    </form>
  );
}

function DomainStep({ onDone }: { onDone: () => void }) {
  const verify = useVerifyDomain(); const set = useSetPanelDomain();
  const [host, setHost] = useState('');
  const { mutate: poll } = set; const status = set.data?.cert_status;
  useEffect(() => { if (status === 'issuing') { const t = setInterval(() => poll(set.data!.hostname), 1500); return () => clearInterval(t); } }, [status, poll, set.data]);
  const v = verify.data; const err = verify.error instanceof ApiError ? verify.error : null;
  return (
    <div className="space-y-3">
      <p className="text-xs text-secondary">Serve the panel over HTTPS on your own domain. Optional: the SSH tunnel keeps working either way, and nothing switches until the certificate is issued.</p>
      <form className="flex gap-2" noValidate onSubmit={(e) => { e.preventDefault(); set.reset(); verify.mutate(host.trim()); }}>
        <div className="flex-1"><Field label="Panel domain" error={err?.fields.hostname}>{(a) => <Input {...a} className="font-mono" placeholder="panel.example.com" value={host} onChange={(e) => setHost(e.target.value)} />}</Field></div>
        <Button type="submit" variant="secondary" className="mt-5" loading={verify.isPending} disabled={!host}>Check DNS</Button>
      </form>
      {v && <div role="status" className={cn('rounded-lg p-3 text-xs', v.matches_host ? 'bg-emerald-500/10 text-emerald-800 dark:text-emerald-300' : 'bg-amber-500/10 text-amber-800 dark:text-amber-300')}><p className="font-semibold">{v.matches_host ? 'DNS points at this server.' : v.resolves ? 'DNS resolves, but not to this server.' : 'DNS does not resolve yet.'}</p><p>{v.detail}</p></div>}
      {v?.matches_host && !set.data && <Button loading={set.isPending} onClick={() => set.mutate(v.hostname)}>Issue certificate</Button>}
      {set.data && <div role="status" className="flex items-center gap-2 rounded-lg border border-subtle bg-surface-subtle p-3 text-xs"><StatusBadge status={set.data.cert_status} /><span className="text-secondary">{set.data.detail}</span></div>}
      {set.isError && <p role="alert" className="text-xs text-red-600">{errorMessage(set.error)}</p>}
      <div className="flex justify-between gap-2"><Button variant="ghost" onClick={onDone}>Skip, use the SSH tunnel</Button><Button disabled={status !== 'issued'} onClick={onDone}>Continue</Button></div>
    </div>
  );
}

function OperatorStep({ onDone, busy }: { onDone: () => void; busy: boolean }) {
  const cands = useOperatorCandidates();
  const set = useSetOperator();
  // has_authorized_keys is unreliable under the daemon's ProtectHome sandbox, so only sudo gates the list.
  const usable = (cands.data?.items ?? []).filter((c) => c.sudo);
  const installOp = usable.find((c) => c.username === 'codeplo-ops');
  // Prefer create when nothing is selectable (common on fresh WSL / single-user hosts).
  const [mode, setMode] = useState<'existing' | 'create'>('existing');
  const [user, setUser] = useState('');
  const [key, setKey] = useState('');
  // Pre-select the operator the installer created so the happy path is a single click.
  useEffect(() => {
    if (mode === 'existing' && !user && installOp) setUser(installOp.username);
  }, [mode, user, installOp]);
  const err = set.error instanceof ApiError ? set.error : null;

  const genCmd = 'ssh-keygen -t ed25519 -C "codeplo-operator" -f ~/.ssh/codeplo_operator -N ""';
  const showCmd = 'cat ~/.ssh/codeplo_operator.pub';

  return (
    <form
      className="space-y-3"
      noValidate
      onSubmit={(e) => {
        e.preventDefault();
        set.mutate(
          { mode, username: user, public_key: mode === 'create' ? key : undefined },
          { onSuccess: onDone },
        );
      }}
    >
      <p className="text-xs text-secondary">
        The host terminal runs as a named, non-root operator. The installer already created <code className="font-mono">codeplo-ops</code> for this — just continue. Advanced: pick
        another sudo user.
      </p>

      <Field label="Operator">
        {(a) => (
          <Select
            {...a}
            value={mode}
            onChange={(e) => {
              setMode(e.target.value as 'existing' | 'create');
              setUser('');
            }}
          >
            <option value="existing">Use an existing user</option>
            <option value="create">Create a new user</option>
          </Select>
        )}
      </Field>

      {mode === 'existing' ? (
        <div className="space-y-2">
          <Field label="Username" error={err?.fields.username}>
            {(a) => (
              <Select {...a} value={user} onChange={(e) => setUser(e.target.value)}>
                <option value="">Select a user…</option>
                {usable.map((c) => (
                  <option key={c.username} value={c.username}>
                    {c.username} (uid {c.uid})
                  </option>
                ))}
              </Select>
            )}
          </Field>
          {usable.length === 0 && (
            <div className="rounded-lg border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-xs text-secondary">
              <p className="font-medium text-primary">No eligible users found</p>
              <p className="mt-1">
                A candidate must be a normal login user (uid ≥ 1000), in the <code className="font-mono">sudo</code> group,
                Re-run the installer as root to create <code className="font-mono">codeplo-ops</code>, or add a sudo user on this host.
              </p>
            </div>
          )}
        </div>
      ) : (
        <div className="space-y-3">
          <div className="rounded-lg border border-subtle bg-surface-muted/60 px-3 py-2.5 text-xs text-secondary space-y-2">
            <p className="font-medium text-primary">Generate an SSH key (one-time)</p>
            <ol className="list-decimal space-y-1.5 pl-4">
              <li>
                In a terminal on this machine (WSL, Git Bash, or PowerShell), run:
                <div className="mt-1 flex items-start gap-2 rounded-md bg-canvas px-2 py-1.5 font-mono text-[11px] text-primary">
                  <code className="flex-1 break-all">{genCmd}</code>
                  <CopyButton value={genCmd} iconOnly label="Copy command" />
                </div>
              </li>
              <li>
                Copy the <strong>public</strong> key:
                <div className="mt-1 flex items-start gap-2 rounded-md bg-canvas px-2 py-1.5 font-mono text-[11px] text-primary">
                  <code className="flex-1 break-all">{showCmd}</code>
                  <CopyButton value={showCmd} iconOnly label="Copy command" />
                </div>
              </li>
              <li>
                Paste the whole line below (starts with <code className="font-mono">ssh-ed25519</code> or{' '}
                <code className="font-mono">ssh-rsa</code>).
              </li>
            </ol>
            <p className="text-[11px] text-muted">
              Keep the private key (<code className="font-mono">codeplo_operator</code>) safe — you will use it if you SSH
              as this operator later. The panel only stores the public half.
            </p>
          </div>

          <Field label="New username" error={err?.fields.username}>
            {(a) => (
              <Input
                {...a}
                className="font-mono"
                placeholder="ops"
                value={user}
                onChange={(e) => setUser(e.target.value)}
              />
            )}
          </Field>
          <Field label="SSH public key" error={err?.fields.public_key}>
            {(a) => (
              <Textarea
                {...a}
                rows={3}
                placeholder="ssh-ed25519 AAAA… codeplo-operator"
                value={key}
                onChange={(e) => setKey(e.target.value)}
              />
            )}
          </Field>
        </div>
      )}

      {err && !['validation_failed'].includes(err.code) && (
        <p role="alert" className="text-xs text-red-600">
          {err.message}
        </p>
      )}
      <div className="flex justify-end">
        <Button type="submit" loading={set.isPending || busy} disabled={!user || (mode === 'create' && !key.trim())}>
          Continue
        </Button>
      </div>
    </form>
  );
}

function HardenStep({ onFinish }: { onFinish: () => void }) {
  const checks = useHardeningChecks(); const audit = useRunAudit();
  useEffect(() => { audit.mutate(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, []);
  return (
    <div className="space-y-3">
      <DeadManBanner />
      <p className="text-xs text-secondary">Your server was audited. Apply the safe fixes now, or do it later from Security. Firewall and SSH changes are guarded and can wait.</p>
      <QueryBoundary query={checks} rows={3}>{(d) => <div className="space-y-2">{d.items.filter((c) => c.tier === 1 || (c.tier === 2 && c.key.startsWith('fw'))).map((c) => <CheckCard key={c.key} check={c} />)}</div>}</QueryBoundary>
      <div className="flex justify-end"><Button onClick={onFinish}>Finish setup</Button></div>
    </div>
  );
}

function ResumeSetupPanel({ initialToken, onResumed }: { initialToken?: string; onResumed: () => void }) {
  const [tok, setTok] = useState(initialToken ?? '');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const cmd = 'sudo -u codeplo codeplo setup-token';
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true); setErr(null);
    try {
      const res = await fetch('/api/v1/setup/resume', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ token: tok.trim() }),
      });
      const body = await res.json().catch(() => ({}));
      if (!res.ok) {
        setErr(typeof body.message === 'string' ? body.message : 'Could not resume setup');
        return;
      }
      onResumed();
    } catch {
      setErr('Network error — is the panel reachable?');
    } finally {
      setBusy(false);
    }
  };
  return (
    <form onSubmit={submit} className="space-y-3" noValidate>
      <div className="rounded-lg border border-amber-500/30 bg-amber-500/5 px-3 py-2.5 text-xs text-secondary space-y-2">
        <p className="font-medium text-primary">Setup session missing</p>
        <p>
          Your browser no longer has the setup cookie (refresh, cleared site data, or a different browser).
          Progress on the server is kept — you only need a fresh token to continue.
        </p>
        <p>On the server run:</p>
        <div className="flex items-start gap-2 rounded-md bg-canvas px-2 py-1.5 font-mono text-[11px] text-primary">
          <code className="flex-1 break-all">{cmd}</code>
          <CopyButton value={cmd} iconOnly label="Copy command" />
        </div>
        <p>Paste the printed token below.</p>
      </div>
      <Field label="Setup token">
        {(a) => (
          <Input
            {...a}
            className="font-mono"
            autoComplete="off"
            value={tok}
            onChange={(e) => setTok(e.target.value)}
            placeholder="paste token from setup-token"
          />
        )}
      </Field>
      {err && <p role="alert" className="text-xs text-red-600">{err}</p>}
      <div className="flex justify-end">
        <Button type="submit" loading={busy} disabled={!tok.trim()}>Resume setup</Button>
      </div>
    </form>
  );
}

export function SetupPage() {
  const nav = useNavigate(); const qc = useQueryClient();
  const { token } = useSearch({ from: '/setup' });
  const state = useSetupState(); const complete = useCompleteSetup();
  const cands = useOperatorCandidates(true);
  const [step, setStep] = useState<Step | null>(null);
  const [sessionLost, setSessionLost] = useState(false);
  const cur = step ?? fromServer(state.data?.step);

  useEffect(() => {
    // Mid-wizard steps need a setup cookie; surface a resume panel when it is gone.
    if (cands.isError && cands.error instanceof ApiError && cands.error.code === 'unauthenticated') {
      setSessionLost(true);
    }
  }, [cands.isError, cands.error]);

  if (state.isPending) return <Shell step={0} title="Loading…"><p className="text-xs text-muted">Checking this server…</p></Shell>;

  const finishSetup = () => complete.mutate(undefined, { onSuccess: async () => { await qc.fetchQuery({ ...meQueryOptions, staleTime: 0 }); setStep(4); } });
  const needsResume = sessionLost && cur > 0 && cur < 4;

  return (
    <Shell step={cur} title={['Create the owner account', 'Turn on two-factor authentication', 'Panel domain', 'Operator identity', 'Harden this server'][cur]}>
      {needsResume ? (
        <ResumeSetupPanel
          initialToken={token}
          onResumed={() => {
            setSessionLost(false);
            void qc.invalidateQueries();
          }}
        />
      ) : (
        <>
          {cur === 0 && <OwnerStep initialToken={token} onDone={() => setStep(1)} />}
          {cur === 1 && <TotpStep onDone={() => setStep(2)} />}
          {cur === 2 && <DomainStep onDone={() => setStep(3)} />}
          {cur === 3 && <OperatorStep busy={complete.isPending} onDone={finishSetup} />}
          {cur === 4 && <HardenStep onFinish={() => void nav({ to: '/' })} />}
        </>
      )}
      {complete.isError && <p role="alert" className="text-xs text-red-600">{errorMessage(complete.error)}</p>}
    </Shell>
  );
}
