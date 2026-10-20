import '@fontsource-variable/plus-jakarta-sans';
import '@fontsource-variable/jetbrains-mono';
import './styles/index.css';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { RouterProvider } from '@tanstack/react-router';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { setAuthHandlers } from '@/api/client';
import { ApiError } from '@/api/errors';
import { resetSession } from '@/api/session';
import { installTransport } from '@/api/transport';
import { ToastProvider } from '@/components/ui/toast';
import { ThemeProvider } from '@/lib/theme';
import { createAppRouter } from './router';

const qc = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      // 4xx answers are decisions, not glitches: never retry them
      retry: (n, e) => !(e instanceof ApiError && e.status >= 400 && e.status < 500) && n < 2,
    },
  },
});
const router = createAppRouter(qc);

// 401 anywhere: drop every cached answer and go to login, remembering where the user was.
let redirecting = false;
setAuthHandlers({
  onUnauthenticated() {
    if (redirecting) return;
    redirecting = true;
    resetSession();
    qc.clear();
    const here = location.pathname + location.search;
    void router.navigate({ to: '/login', search: { next: here === '/login' ? undefined : here } }).finally(() => { redirecting = false; });
  },
});

void installTransport().then(() => {
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <ThemeProvider><ToastProvider><QueryClientProvider client={qc}><RouterProvider router={router} /></QueryClientProvider></ToastProvider></ThemeProvider>
    </StrictMode>,
  );
});
