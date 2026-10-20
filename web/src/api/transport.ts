import { API_MODE } from './client';

/**
 * Installs the mock transport (fetch / EventSource / WebSocket replacements) in `mock` mode only.
 * The condition is a compile-time constant, so a `live` production build tree-shakes the dynamic
 * import away and contains zero mock code (verified by `npm run check:mock-free`).
 */
export async function installTransport(): Promise<void> {
  if (import.meta.env.VITE_API_MODE === 'mock') {
    const m = await import('../mock/install');
    await m.installMock();
  }
  void API_MODE;
}
