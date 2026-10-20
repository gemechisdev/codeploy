import { EyeOff, FileText, Plus, Trash2 } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { useEnv, usePutEnv } from '@/api/hooks/workloads';
import { ApiError, errorMessage } from '@/api/errors';
import type { Resource } from '@/api/types';
import { QueryBoundary } from '@/components/app/QueryBoundary';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardBody, CardHeader } from '@/components/ui/card';
import { Dialog } from '@/components/ui/dialog';
import { Input, Select, Textarea } from '@/components/ui/input';
import { useToast } from '@/components/ui/toast';
import { usePermissions } from '@/lib/permissions';

type Scope = 'build' | 'runtime' | 'both';
interface Row { uid: number; key: string; value: string; scope: Scope; is_secret: boolean; existing: boolean; replacing: boolean }
let uid = 0;

/** Secrets are write-only (plan 3.9): the API never returns a value, so the UI cannot show one. Rows can only keep, replace or delete. */
export function EnvTab({ resource }: { resource: Resource }) {
  const q = useEnv(resource.id);
  const put = usePutEnv(resource.id);
  const toast = useToast();
  const { can } = usePermissions();
  const canWrite = can('env:write');
  const [rows, setRows] = useState<Row[]>([]);
  const [dirty, setDirty] = useState(false);
  const [paste, setPaste] = useState(false); const [pasteText, setPasteText] = useState(''); const [pasteSecret, setPasteSecret] = useState(true);
  useEffect(() => {
    if (q.data && !dirty) setRows(q.data.items.map((e) => ({ uid: ++uid, key: e.key, value: e.is_secret ? '' : (e.value ?? ''), scope: e.scope as Scope, is_secret: e.is_secret, existing: true, replacing: false })));
  }, [q.data, dirty]);
  const edit = (id: number, p: Partial<Row>) => { setDirty(true); setRows((r) => r.map((x) => (x.uid === id ? { ...x, ...p } : x))); };
  const err = put.error instanceof ApiError ? put.error : null;
  const fe = (i: number, f: string) => err?.fields[`items.${i}.${f}`];
  const willRotate = useMemo(() => rows.some((r) => r.existing && r.is_secret && r.replacing), [rows]);

  const save = () => put.mutate(rows.map((r) => ({
    key: r.key.trim(), scope: r.scope, is_secret: r.is_secret,
    value: r.is_secret ? (r.existing && !r.replacing ? undefined : r.value) : r.value,
    keep_existing_secret: r.is_secret && r.existing && !r.replacing ? true : undefined,
  })), { onSuccess: () => { setDirty(false); toast.success('Environment saved. Changes apply on the next deployment.'); }, onError: (e) => { if (!(e instanceof ApiError && e.code === 'validation_failed')) toast.error(errorMessage(e)); } });

  const importPaste = () => {
    const add: Row[] = [];
    for (const line of pasteText.split('\n')) {
      const m = /^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*?)\s*$/.exec(line);
      if (m && !line.trim().startsWith('#')) add.push({ uid: ++uid, key: m[1], value: m[2].replace(/^(['"])(.*)\1$/, '$2'), scope: 'runtime', is_secret: pasteSecret, existing: false, replacing: false });
    }
    setDirty(true);
    setRows((r) => [...r.filter((x) => !add.some((a) => a.key === x.key)), ...add]);
    setPaste(false); setPasteText('');
    toast.success(`${add.length} variable${add.length === 1 ? '' : 's'} added. Review, then save.`);
  };

  return (
    <Card>
      <CardHeader title="Environment variables" description="Secret values are write-only: after saving they are never displayed again, by anyone."
        actions={canWrite && <><Button variant="secondary" size="sm" onClick={() => setPaste(true)}><FileText className="size-3.5" />Paste .env</Button><Button variant="secondary" size="sm" onClick={() => { setDirty(true); setRows((r) => [...r, { uid: ++uid, key: '', value: '', scope: 'runtime', is_secret: false, existing: false, replacing: false }]); }}><Plus className="size-3.5" />Add</Button></>} />
      <QueryBoundary query={q} rows={3}>
        {() => (
          <CardBody className="space-y-2">
            {rows.length === 0 && <p className="py-6 text-center text-xs text-muted">No variables yet.</p>}
            {rows.map((r, i) => (
              <div key={r.uid} className="grid items-start gap-2 rounded-lg border border-subtle bg-surface-subtle p-2.5 sm:grid-cols-[1.1fr_1.6fr_7rem_auto_auto]">
                <div><Input aria-label={`Key ${i + 1}`} aria-invalid={!!fe(i, 'key')} className="font-mono" placeholder="KEY" value={r.key} readOnly={r.existing || !canWrite} onChange={(e) => edit(r.uid, { key: e.target.value })} />{fe(i, 'key') && <p role="alert" className="mt-1 text-[11px] text-red-600">{fe(i, 'key')}</p>}</div>
                <div>
                  {r.is_secret && r.existing && !r.replacing ? (
                    <div className="flex items-center gap-2 rounded-lg border border-dashed border-strong px-3 py-1.5 text-xs text-muted"><EyeOff aria-hidden className="size-3.5" /><span className="flex-1">Hidden · write-only</span>{canWrite && <button className="cursor-pointer font-semibold text-brand-primary hover:underline" onClick={() => edit(r.uid, { replacing: true, value: '' })}>Replace</button>}</div>
                  ) : (
                    <Input aria-label={`Value ${i + 1}`} aria-invalid={!!fe(i, 'value')} className="font-mono" type={r.is_secret ? 'password' : 'text'} autoComplete="new-password" placeholder={r.is_secret ? 'New secret value' : 'value'} value={r.value} readOnly={!canWrite} onChange={(e) => edit(r.uid, { value: e.target.value })} />
                  )}
                  {fe(i, 'value') && <p role="alert" className="mt-1 text-[11px] text-red-600">{fe(i, 'value')}</p>}
                </div>
                <Select aria-label={`Scope ${i + 1}`} value={r.scope} disabled={!canWrite} onChange={(e) => edit(r.uid, { scope: e.target.value as Scope })}><option value="runtime">runtime</option><option value="build">build</option><option value="both">both</option></Select>
                <label className="flex items-center gap-1.5 pt-1.5 text-[11px] text-secondary"><input type="checkbox" className="size-3.5 accent-brand-primary" checked={r.is_secret} disabled={!canWrite || (r.existing && r.is_secret)} onChange={(e) => edit(r.uid, { is_secret: e.target.checked })} />Secret</label>
                {canWrite && <Button variant="ghost" size="icon" aria-label={`Remove ${r.key || `row ${i + 1}`}`} onClick={() => { setDirty(true); setRows((x) => x.filter((y) => y.uid !== r.uid)); }}><Trash2 className="size-3.5 text-red-500" /></Button>}
                {fe(i, 'scope') && <p role="alert" className="text-[11px] text-red-600 sm:col-span-5">{fe(i, 'scope')}</p>}
              </div>
            ))}
            {canWrite && (
              <div className="flex flex-wrap items-center justify-between gap-2 pt-2">
                <p className="text-[11px] text-muted">{willRotate ? <Badge tone="amber">Rotating a secret asks for a fresh 2FA code</Badge> : 'Changes apply on the next deployment.'}</p>
                <div className="flex gap-2">{dirty && <Button variant="secondary" onClick={() => { setDirty(false); put.reset(); void q.refetch(); }}>Discard</Button>}<Button disabled={!dirty} loading={put.isPending} onClick={save}>Save changes</Button></div>
              </div>
            )}
          </CardBody>
        )}
      </QueryBoundary>
      <Dialog open={paste} onOpenChange={setPaste} title="Paste .env" description="One KEY=value per line. Comments and blank lines are ignored."
        footer={<><Button variant="secondary" onClick={() => setPaste(false)}>Cancel</Button><Button disabled={!pasteText.trim()} onClick={importPaste}>Add variables</Button></>}>
        <div className="space-y-3"><Textarea aria-label=".env contents" rows={8} placeholder={'DATABASE_URL=postgres://…\nLOG_LEVEL=info'} value={pasteText} onChange={(e) => setPasteText(e.target.value)} />
          <label className="flex cursor-pointer items-center gap-2 text-xs text-primary"><input type="checkbox" className="size-4 accent-brand-primary" checked={pasteSecret} onChange={(e) => setPasteSecret(e.target.checked)} />Mark all as secret (recommended)</label></div>
      </Dialog>
    </Card>
  );
}
