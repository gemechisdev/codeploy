import { Outlet } from '@tanstack/react-router';
import { useQueryClient } from '@tanstack/react-query';
import { lazy, Suspense, useEffect, useState } from 'react';
import { connectEvents } from '@/api/events';
import { useMe } from '@/api/hooks/auth';
import { DeadManBanner } from '@/components/app/DeadManBanner';
import { SkeletonRows } from '@/components/ui/skeleton';
import { CommandPalette } from './CommandPalette';
import { Header } from './Header';
import { Sidebar } from './Sidebar';

// Dev/demo only. The condition is a build-time constant: a live production build drops this import entirely.
const DemoToolbar = import.meta.env.VITE_API_MODE === 'mock' ? lazy(() => import('@/mock/DemoToolbar')) : null;

export function AppShell() {
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
      <a href="#main" className="sr-only focus:not-sr-only focus:fixed focus:left-2 focus:top-2 focus:z-[70] focus:rounded-lg focus:bg-brand-primary focus:px-3 focus:py-2 focus:text-xs focus:text-white">Skip to content</a>
      <aside className="fixed inset-y-0 left-0 z-40 hidden w-60 border-r border-subtle lg:block"><Sidebar /></aside>
      {drawer && (
        <div className="fixed inset-0 z-50 lg:hidden" role="dialog" aria-modal="true" aria-label="Navigation">
          <button aria-label="Close navigation" className="absolute inset-0 bg-black/50" onClick={() => setDrawer(false)} />
          <div className="absolute inset-y-0 left-0 w-64 border-r border-subtle shadow-xl"><Sidebar onNavigate={() => setDrawer(false)} /></div>
        </div>
      )}
      <Header onMenu={() => setDrawer(true)} onPalette={() => setPalette(true)} />
      {me.data?.bootstrap && <div role="status" className="border-b border-brand-primary/30 bg-brand-primary/10 px-4 py-2 text-center text-xs text-brand-primary">Bootstrap session (SSH tunnel). Configure HTTPS in Settings to serve the panel on a domain.</div>}
      <DeadManBanner />
      <main id="main" className="mx-auto w-full max-w-7xl p-3 sm:p-5"><Suspense fallback={<SkeletonRows rows={6} />}><Outlet /></Suspense></main>
      <CommandPalette open={palette} onOpenChange={setPalette} />
      {DemoToolbar && <Suspense fallback={null}><DemoToolbar /></Suspense>}
    </div>
  );
}
