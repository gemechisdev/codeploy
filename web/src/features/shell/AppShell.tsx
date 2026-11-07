import * as RD from '@radix-ui/react-dialog';
import { Outlet } from '@tanstack/react-router';
import { useQueryClient } from '@tanstack/react-query';
import { lazy, Suspense, useEffect, useState } from 'react';
import { connectEvents } from '@/api/events';
import { useMe } from '@/api/hooks/auth';
import { DeadManBanner } from '@/components/app/DeadManBanner';
import { SkeletonRows } from '@/components/ui/skeleton';
import { useT } from '@/i18n';
import { CommandPalette } from './CommandPalette';
import { Header } from './Header';
import { Sidebar } from './Sidebar';

// Dev/demo only. The condition is a build-time constant: a live production build drops this import entirely.
const DemoToolbar = import.meta.env.VITE_API_MODE === 'mock' ? lazy(() => import('@/mock/DemoToolbar')) : null;

export function AppShell() {
  const t = useT();
  const qc = useQueryClient();
  const me = useMe();
  const [drawer, setDrawer] = useState(false);
  const [palette, setPalette] = useState(false);
  useEffect(() => { const c = connectEvents(qc); return () => c.close(); }, [qc]); // one app-wide SSE connection, created after login
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); setPalette((p) => !p); } };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);
  return (
    <div className="min-h-screen lg:pl-60">
      <a href="#main" className="sr-only focus:not-sr-only focus:fixed focus:left-2 focus:top-2 focus:z-[70] focus:rounded-lg focus:bg-brand-primary focus:px-3 focus:py-2 focus:text-xs focus:text-white focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand-primary">{t('shell.skipToContent')}</a>
      <aside className="fixed inset-y-0 left-0 z-40 hidden w-60 border-r border-subtle lg:block"><Sidebar /></aside>
      <RD.Root open={drawer} onOpenChange={setDrawer}>
        <RD.Portal>
          <RD.Overlay className="fixed inset-0 z-50 bg-black/50 lg:hidden" />
          <RD.Content className="fixed inset-y-0 left-0 z-50 w-64 border-r border-subtle bg-surface shadow-xl focus:outline-none lg:hidden">
            <RD.Title className="sr-only">{t('shell.navigation')}</RD.Title>
            <RD.Description className="sr-only">{t('shell.navigationDescription')}</RD.Description>
            <Sidebar onNavigate={() => setDrawer(false)} />
          </RD.Content>
        </RD.Portal>
      </RD.Root>
      <Header onMenu={() => setDrawer(true)} onPalette={() => setPalette(true)} />
      {me.data?.bootstrap && <div role="status" className="border-b border-brand-primary/30 bg-brand-primary/10 px-4 py-2 text-center text-xs text-brand-primary">{t('shell.bootstrap')}</div>}
      <DeadManBanner />
      <main id="main" tabIndex={-1} className="mx-auto w-full max-w-7xl p-3 sm:p-5"><Suspense fallback={<SkeletonRows rows={6} />}><Outlet /></Suspense></main>
      <CommandPalette open={palette} onOpenChange={setPalette} />
      {DemoToolbar && <Suspense fallback={null}><DemoToolbar /></Suspense>}
    </div>
  );
}
