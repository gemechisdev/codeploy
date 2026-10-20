import { useNavigate, useSearch } from '@tanstack/react-router';
import { KeyRound, Lock } from 'lucide-react';
import { lazy, Suspense, useState, type FormEvent } from 'react';
import { useLogin, useVerifyTotp } from '@/api/hooks/auth';
import { ApiError } from '@/api/errors';
import { Button } from '@/components/ui/button';
import { Card, CardBody } from '@/components/ui/card';
import { Field, Input } from '@/components/ui/input';

const DemoHint = import.meta.env.VITE_API_MODE === 'mock' ? lazy(() => import('@/mock/DemoHint')) : null;

function describe(e: unknown, field: string): { form?: string; field?: string } {
  if (!(e instanceof ApiError)) return {};
  if (e.code === 'rate_limited') return { form: `Too many attempts. Try again in ${e.retryAfter ?? 30} seconds.` };
  if (e.code === 'validation_failed') return { field: e.fields[field] ?? e.message };
  if (e.code === 'network') return { form: e.message };
  return { form: e.message };
}

export function LoginPage() {
  const nav = useNavigate();
  const { next } = useSearch({ from: '/login' });
  const login = useLogin();
  const verify = useVerifyTotp();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const [challenge, setChallenge] = useState<string | null>(null);
  const go = () => void nav({ to: (next ?? '/') as '/' });

  const onPassword = (e: FormEvent) => {
    e.preventDefault();
    login.mutate({ email: email.trim(), password }, { onSuccess: (r) => (r.totp_required && r.challenge ? setChallenge(r.challenge) : go()) });
  };
  const onCode = (e: FormEvent) => {
    e.preventDefault();
    if (challenge) verify.mutate({ challenge, code }, { onSuccess: go, onError: (err) => { if (err instanceof ApiError && err.status === 401) { setChallenge(null); setCode(''); } } });
  };
  const pe = describe(login.error, 'password'); const ve = describe(verify.error, 'code');

  return (
    <div className="flex min-h-screen items-center justify-center bg-canvas p-4">
      <div className="w-full max-w-sm space-y-4">
        <div className="flex flex-col items-center gap-2 text-center">
          <span className="flex size-12 items-center justify-center rounded-xl bg-gradient-to-br from-brand-primary to-brand-secondary font-mono text-sm font-bold text-white shadow-lg">c/p</span>
          <h1 className="text-xl font-bold tracking-tight text-primary">Sign in to codeplo</h1>
          <p className="text-xs text-muted">{challenge ? 'Enter the 6-digit code from your authenticator app.' : 'Your self-hosted deployment control plane.'}</p>
        </div>
        <Card>
          <CardBody>
            {!challenge ? (
              <form onSubmit={onPassword} className="space-y-3" noValidate>
                {pe.form && <p role="alert" className="rounded-lg bg-red-500/10 p-2.5 text-xs text-red-700 dark:text-red-400">{pe.form}</p>}
                <Field label="Email">{(a) => <Input {...a} type="email" autoComplete="username" autoFocus required value={email} onChange={(e) => setEmail(e.target.value)} />}</Field>
                <Field label="Password">{(a) => <Input {...a} type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />}</Field>
                <Button type="submit" className="w-full" loading={login.isPending} disabled={!email || !password}><Lock className="size-3.5" />Continue</Button>
              </form>
            ) : (
              <form onSubmit={onCode} className="space-y-3" noValidate>
                {ve.form && <p role="alert" className="rounded-lg bg-red-500/10 p-2.5 text-xs text-red-700 dark:text-red-400">{ve.form}</p>}
                <Field label="Authenticator code" error={ve.field}>
                  {(a) => <Input {...a} autoFocus inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))} className="text-center font-mono text-lg tracking-[0.5em]" />}
                </Field>
                <Button type="submit" className="w-full" loading={verify.isPending} disabled={code.length !== 6}><KeyRound className="size-3.5" />Verify and sign in</Button>
                <Button variant="ghost" className="w-full" onClick={() => { setChallenge(null); setCode(''); verify.reset(); }}>Use a different account</Button>
              </form>
            )}
          </CardBody>
        </Card>
        {DemoHint && <Suspense fallback={null}><DemoHint /></Suspense>}
      </div>
    </div>
  );
}
