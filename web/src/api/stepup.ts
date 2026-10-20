/**
 * Step-up coordination. The server decides when step-up is needed (403 step_up_required);
 * the client only opens the dialog, then retries the original request once.
 */
type Listener = () => void;

let open = false;
let waiters: ((ok: boolean) => void)[] = [];
const listeners = new Set<Listener>();
const emit = () => listeners.forEach((l) => l());

export const stepUpStore = {
  subscribe(l: Listener) {
    listeners.add(l);
    return () => listeners.delete(l);
  },
  getSnapshot: () => open,
  /** Resolves true once the user has completed step-up, false if cancelled. */
  request(): Promise<boolean> {
    return new Promise((resolve) => {
      waiters.push(resolve);
      if (!open) {
        open = true;
        emit();
      }
    });
  },
  settle(ok: boolean) {
    const w = waiters;
    waiters = [];
    open = false;
    emit();
    w.forEach((r) => r(ok));
  },
};
