import { ShieldCheck } from 'lucide-react';
import { useState, useSyncExternalStore, type FormEvent } from 'react';
import { stepUpStore } from '@/api/stepup';
import { useSettings } from '@/api/hooks/core';
import { useStepUpMutation } from '@/api/hooks/auth';
import { ApiError } from '@/api/errors';
import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { Field, Input } from '@/components/ui/input';

/** Global dialog opened by the API client on `403 step_up_required`. The server validates the TOTP code; nothing here decides. */
export function StepUpDialog() {
  const open = useSyncExternalStore(stepUpStore.subscribe, stepUpStore.getSnapshot);
  const [code, setCode] = useState('');
  const m = useStepUpMutation();
  const { data } = useSettings();
  const minutes = Math.round(Number(data?.values.step_up_window_s ?? 300) / 60);
  const close = (ok: boolean) => { setCode(''); m.reset(); stepUpStore.settle(ok); };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    m.mutate(code, { onSuccess: () => close(true) });
  };
  const err = m.error instanceof ApiError ? (m.error.fields.code ?? m.error.message) : undefined;
  return (
    <Dialog open={open} onOpenChange={(o) => !o && close(false)} title="Confirm it's you" size="sm"
      description={`Enter your authenticator code to continue. Elevation lasts ${minutes} minute${minutes === 1 ? '' : 's'}.`}>
      <form onSubmit={submit} className="space-y-3">
        <div className="flex items-center gap-2 rounded-lg bg-brand-primary/10 p-2.5 text-xs text-brand-primary"><ShieldCheck aria-hidden className="size-4 shrink-0" />This action needs a fresh two-factor check.</div>
        <Field label="6-digit code" error={err}>
          {(a) => <Input {...a} autoFocus inputMode="numeric" autoComplete="one-time-code" maxLength={6} pattern="\d{6}" value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))} className="text-center font-mono text-base tracking-[0.4em]" />}
        </Field>
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={() => close(false)}>Cancel</Button>
          <Button type="submit" loading={m.isPending} disabled={code.length !== 6}>Verify</Button>
        </div>
      </form>
    </Dialog>
  );
}
