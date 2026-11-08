import { useState } from 'react';
import { Send, Trash2 } from 'lucide-react';
import { useAlertRules, useChannels, useCreateChannel, useDeleteChannel, usePatchAlertRule, usePatchChannel, useTestChannel } from '@/api/hooks/ops';
import { errorMessage } from '@/api/errors';
import type { Schema } from '@/api/types';
import { ConfirmDialog } from '@/components/app/ConfirmDialog';
import { QueryBoundary } from '@/components/app/QueryBoundary';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardBody, CardHeader } from '@/components/ui/card';
import { Dialog } from '@/components/ui/dialog';
import { Field, Input, Select } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import { useToast } from '@/components/ui/toast';
import { relTime } from '@/lib/format';
import { usePermissions } from '@/lib/permissions';
import { ModuleGate, PageHeader } from '@/features/shell/ModuleGate';

type Kind = Schema<'CreateChannelRequest'>['kind'];
const FIELDS: Record<Kind, { key: string; label: string; secret?: boolean; placeholder?: string }[]> = {
  telegram: [{ key: 'bot_token', label: 'Bot token', secret: true }, { key: 'chat_id', label: 'Chat ID' }],
  discord: [{ key: 'webhook_url', label: 'Webhook URL', secret: true }],
  email: [
    { key: 'to', label: 'Recipient address', placeholder: 'alerts@example.com' },
    { key: 'from', label: 'Sender address', placeholder: 'codeplo@example.com' },
    { key: 'host', label: 'SMTP host', placeholder: 'smtp.example.com' },
    { key: 'port', label: 'Port', placeholder: '587' },
    { key: 'username', label: 'Username (optional)' },
    { key: 'password', label: 'Password', secret: true },
    { key: 'tls', label: 'TLS mode', placeholder: 'starttls | ssl | plain' },
  ],
  webhook: [{ key: 'url', label: 'URL', secret: true }],
};

function AddChannel({ onClose }: { onClose: () => void }) {
  const create = useCreateChannel(); const toast = useToast();
  const [kind, setKind] = useState<Kind>('telegram'); const [name, setName] = useState(''); const [config, setConfig] = useState<Record<string, string>>({});
  // Required keys per kind. Email: backend requires to/from/host/password; port/username/tls have defaults.
  const required: Record<Kind, string[]> = {
    telegram: ['bot_token', 'chat_id'],
    discord: ['webhook_url'],
    email: ['to', 'from', 'host', 'password'],
    webhook: ['url'],
  };
  const ready = name.trim() && required[kind].every((k) => config[k]?.trim());
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Add channel" description="Credentials are write-only: once saved, only a hint of the destination is shown."
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button>
        <Button loading={create.isPending} disabled={!ready} onClick={() => create.mutate({ kind, name: name.trim(), config }, { onSuccess: () => { toast.success('Channel added.'); onClose(); }, onError: (e) => toast.error(errorMessage(e)) })}>Add channel</Button></>}>
      <div className="space-y-3">
        <Field label="Type">{(a) => <Select {...a} value={kind} onChange={(e) => { setKind(e.target.value as Kind); setConfig({}); }}>{(Object.keys(FIELDS) as Kind[]).map((k) => <option key={k} value={k}>{k}</option>)}</Select>}</Field>
        <Field label="Name">{(a) => <Input {...a} value={name} onChange={(e) => setName(e.target.value)} />}</Field>
        {FIELDS[kind].map((f) => <Field key={f.key} label={f.label}>{(a) => <Input {...a} type={f.secret ? 'password' : 'text'} autoComplete="off" placeholder={f.placeholder} value={config[f.key] ?? ''} onChange={(e) => setConfig({ ...config, [f.key]: e.target.value })} />}</Field>)}
      </div>
    </Dialog>
  );
}

export function NotificationsPage() {
  const channels = useChannels(); const rules = useAlertRules(); const patchCh = usePatchChannel(); const del = useDeleteChannel(); const test = useTestChannel(); const patchRule = usePatchAlertRule();
  const toast = useToast(); const { can } = usePermissions(); const manage = can('notify:manage');
  const [adding, setAdding] = useState(false); const [delId, setDelId] = useState<string | null>(null);
  const err = (e: unknown) => toast.error(errorMessage(e));
  return (
    <ModuleGate module="notify">
      <PageHeader title="Notifications" description="Where alerts are delivered, and which conditions raise them." actions={manage && <Button onClick={() => setAdding(true)}>Add channel</Button>} />
      <div className="space-y-4">
        <Card><CardHeader title="Channels" />
          <QueryBoundary query={channels} isEmpty={(d) => !d?.items?.length} empty={<p className="p-6 text-center text-xs text-muted">No channels yet. Add one to receive alerts.</p>} rows={2}>
            {(d) => <ul className="divide-y divide-subtle">{d.items.map((c) => (
              <li key={c.id} className="flex flex-wrap items-center gap-3 px-4 py-3 text-xs">
                <div className="min-w-0 flex-1"><p className="font-semibold text-primary">{c.name} <Badge tone="brand">{c.kind}</Badge></p><p className="font-mono text-[11px] text-muted">{c.destination_hint}</p></div>
                <Switch aria-label={`${c.name} enabled`} checked={c.enabled} disabled={!manage} onCheckedChange={(enabled) => patchCh.mutate({ id: c.id, body: { enabled } }, { onError: err })} />
                {manage && <>
                  <Button size="sm" variant="secondary" loading={test.isPending && test.variables === c.id} onClick={() => test.mutate(c.id, { onSuccess: () => toast.success('Test message sent.'), onError: err })}><Send className="size-3" />Test</Button>
                  <Button size="sm" variant="secondary" aria-label={`Delete ${c.name}`} onClick={() => setDelId(c.id)}><Trash2 className="size-3" /></Button></>}
              </li>))}</ul>}
          </QueryBoundary></Card>
        <Card><CardHeader title="Alert rules" />
          <QueryBoundary query={rules} isEmpty={(d) => !d?.items?.length} empty={<p className="p-6 text-center text-xs text-muted">No alert rules.</p>} rows={3}>
            {(d) => {
              const chItems = channels.data?.items ?? [];
              return (
              <CardBody className="p-0"><ul className="divide-y divide-subtle">{d.items.map((r) => (
                <li key={r.id} className="flex flex-col gap-2 px-4 py-3 text-xs">
                  <div className="flex flex-wrap items-center gap-3">
                    <div className="min-w-0 flex-1">
                      <p className="font-semibold text-primary">{r.title}
                        {r.enabled && !(r.channel_ids?.length) && (
                          <Badge tone="amber" className="ml-1.5 align-middle">no channels</Badge>
                        )}
                      </p>
                      <p className="text-muted">{r.condition} · threshold {r.threshold} · cooldown {r.cooldown_s}s · {r.last_triggered_at ? `last fired ${relTime(r.last_triggered_at)}` : 'never fired'}</p>
                      {r.enabled && !(r.channel_ids?.length) && (
                        <p className="mt-0.5 text-[11px] text-amber-700 dark:text-amber-300">Enabled but no channel selected — firings are recorded, nothing is delivered.</p>
                      )}
                    </div>
                    <Switch aria-label={`${r.title} enabled`} checked={r.enabled} disabled={!manage} onCheckedChange={(enabled) => patchRule.mutate({ id: r.id, body: { enabled } }, { onError: err })} />
                  </div>
                  {manage && (
                    <div className="flex flex-wrap items-center gap-2 pl-0.5">
                      <span className="text-[11px] text-muted">Notify:</span>
                      {chItems.length === 0 ? (
                        <span className="text-[11px] text-muted">Add a channel above first.</span>
                      ) : chItems.map((c) => {
                        const on = (r.channel_ids ?? []).includes(c.id);
                        return (
                          <label key={c.id} className="inline-flex cursor-pointer items-center gap-1 rounded border border-subtle px-1.5 py-0.5 text-[11px]">
                            <input
                              type="checkbox"
                              className="size-3 accent-brand-primary"
                              checked={on}
                              disabled={patchRule.isPending}
                              onChange={() => {
                                const next = on
                                  ? (r.channel_ids ?? []).filter((id) => id !== c.id)
                                  : [...(r.channel_ids ?? []), c.id];
                                patchRule.mutate({ id: r.id, body: { channel_ids: next } }, {
                                  onSuccess: () => toast.success(on ? `Removed ${c.name}` : `Added ${c.name}`),
                                  onError: err,
                                });
                              }}
                            />
                            {c.name}
                          </label>
                        );
                      })}
                    </div>
                  )}
                  {!manage && (r.channel_ids?.length ?? 0) > 0 && (
                    <p className="text-[11px] text-muted">Channels: {(r.channel_ids ?? []).map((id) => chItems.find((c) => c.id === id)?.name ?? id).join(', ')}</p>
                  )}
                </li>))}</ul></CardBody>
              );
            }}
          </QueryBoundary></Card>
      </div>
      {adding && <AddChannel onClose={() => setAdding(false)} />}
      <ConfirmDialog open={!!delId} onOpenChange={(o) => !o && setDelId(null)} destructive title="Delete channel?" confirmLabel="Delete" loading={del.isPending} description="Alert rules that used this channel will stop notifying it."
        onConfirm={() => delId && del.mutate(delId, { onSuccess: () => { toast.success('Channel deleted.'); setDelId(null); }, onError: err })} />
    </ModuleGate>
  );
}
