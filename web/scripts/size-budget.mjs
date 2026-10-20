// Initial JS <= 250 KB gzip; every lazy chunk <= 120 KB gzip (brief WP14).
import { readdirSync, readFileSync, existsSync } from 'node:fs';
import { gzipSync } from 'node:zlib';
import { join } from 'node:path';

const DIST = new URL('../dist', import.meta.url).pathname;
const ASSETS = join(DIST, 'assets');
if (!existsSync(ASSETS)) { console.error('dist/assets not found: run `npm run build` first'); process.exit(2); }
const html = readFileSync(join(DIST, 'index.html'), 'utf8');
const initial = new Set([...html.matchAll(/(?:src|href)="\/?(assets\/[^"]+\.js)"/g)].map((m) => m[1].replace('assets/', '')));
const kb = (b) => b / 1024;
let initialKb = 0; const over = []; const rows = [];
for (const f of readdirSync(ASSETS).filter((n) => n.endsWith('.js'))) {
  const g = kb(gzipSync(readFileSync(join(ASSETS, f))).length);
  if (initial.has(f)) initialKb += g;
  else { rows.push([f, g]); if (g > 120) over.push(`${f}: ${g.toFixed(1)} KB gzip > 120 KB`); }
}
console.log(`initial JS: ${initialKb.toFixed(1)} KB gzip (budget 250)`);
for (const [f, g] of rows.sort((a, b) => b[1] - a[1]).slice(0, 8)) console.log(`  lazy ${f}: ${g.toFixed(1)} KB gzip`);
if (initialKb > 250) over.push(`initial bundle ${initialKb.toFixed(1)} KB gzip > 250 KB`);
if (over.length) { console.error('check:size FAILED\n' + over.join('\n')); process.exit(1); }
console.log('check:size ok');
