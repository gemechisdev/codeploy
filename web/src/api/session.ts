/** Tiny non-React session state: CSRF token and server clock offset. */
let csrfToken = '';
let offsetMs = 0;

export function setCsrf(token: string) {
  csrfToken = token;
}
export function getCsrf() {
  return csrfToken;
}
/** Record server time so countdowns never depend on the client clock. */
export function syncServerNow(iso: string) {
  const t = Date.parse(iso);
  if (Number.isFinite(t)) offsetMs = t - Date.now();
}
export function serverNowMs() {
  return Date.now() + offsetMs;
}
export function resetSession() {
  csrfToken = '';
}
