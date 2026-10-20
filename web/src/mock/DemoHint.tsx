/** Shown on the login page in mock mode only. Lives in src/mock so credentials never reach a live bundle. */
export default function DemoHint() {
  return (
    <div role="note" className="rounded-lg border border-brand-accent/40 bg-brand-accent/10 p-3 text-[11px] text-secondary">
      <p className="mb-1 font-bold text-brand-accent-dark dark:text-brand-accent">Demo mode</p>
      <p>Sign in with <code className="font-mono text-primary">owner@example.com</code> / <code className="font-mono text-primary">demo-password-change-me</code>, then enter code <code className="font-mono text-primary">123456</code>.</p>
      <p className="mt-1">Other personas: admin@, deployer@, viewer@example.com (same password). Add <code className="font-mono">?mock=fresh</code> to the URL to run the first-run wizard.</p>
    </div>
  );
}
