// A `live` production build must contain zero mock code (brief 4.2). Run after `VITE_API_MODE=live npm run build`.
import { readdirSync, readFileSync, statSync, existsSync } from 'node:fs';
import { join, extname } from 'node:path';
import { fileURLToPath } from 'node:url';

const DIST = fileURLToPath(new URL('../dist', import.meta.url));
if (!existsSync(DIST)) { console.error('dist/ not found'); process.exit(2); }
// Strings that only exist in src/mock/** (seeds, demo credentials, mock internals).
const MARKERS = ['owner@example.com', 'demo-password-change-me', 'demo-setup-token', '__codeploMock', 'MockEventSource', 'MockWebSocket', 'Injected server error', 'mock shell: no real commands run', 'DemoToolbar'];
const hits = [];
(function walk(d) {
  for (const n of readdirSync(d)) {
    const p = join(d, n);
    if (statSync(p).isDirectory()) walk(p);
    else if (['.js', '.html', '.css', '.map'].includes(extname(p))) {
      const s = readFileSync(p, 'utf8');
      for (const m of MARKERS) if (s.includes(m)) hits.push(`${p.replace(DIST, 'dist')}: ${m}`);
    }
  }
})(DIST);
if (hits.length) { console.error('check:mock-free FAILED: mock code leaked into the live bundle\n' + hits.join('\n')); process.exit(1); }
console.log('check:mock-free ok: no src/mock strings in dist/');
