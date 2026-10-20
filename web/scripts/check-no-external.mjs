// Fails if dist/ references a third-party host (brief: no external hosts at runtime).
import { readdirSync, readFileSync, statSync, existsSync } from 'node:fs';
import { join, extname } from 'node:path';
import { fileURLToPath } from 'node:url';

const DIST = fileURLToPath(new URL('../dist', import.meta.url));
if (!existsSync(DIST)) { console.error('dist/ not found: run `npm run build` first'); process.exit(2); }

// Hosts that may appear as inert strings (XML namespaces, spec URLs inside library comments, docs placeholders).
const ALLOW = [/^localhost$/, /^127\.0\.0\.1$/, /(^|\.)example\.com$/, /^www\.w3\.org$/, /^reactjs\.org$/, /^react\.dev$/, /^github\.com$/, /^fb\.me$/];
const TEXT = new Set(['.html', '.js', '.css', '.mjs', '.json', '.svg', '.webmanifest', '.txt']);
const bad = [];

function walk(dir) {
  for (const n of readdirSync(dir)) {
    const p = join(dir, n);
    if (statSync(p).isDirectory()) walk(p);
    else if (TEXT.has(extname(p))) scan(p);
  }
}
function scan(file) {
  const src = readFileSync(file, 'utf8');
  for (const m of src.matchAll(/(?:https?:)?\/\/([a-z0-9][a-z0-9.-]*\.[a-z]{2,})(?::\d+)?(\/[^\s"'`)<>]*)?/gi)) {
    const host = m[1].toLowerCase();
    if (ALLOW.some((r) => r.test(host))) continue;
    // `//host` inside a regexp or comment is common in bundles; only flag explicit schemes or src/href/url() contexts.
    const ctx = src.slice(Math.max(0, m.index - 12), m.index + m[0].length);
    if (!/https?:\/\//i.test(m[0]) && !/(src|href|url\(|import\()\s*=?\s*["']?$/i.test(ctx.slice(0, ctx.length - m[0].length))) continue;
    bad.push(`${file.replace(DIST, 'dist')}: ${m[0].slice(0, 90)}`);
  }
}
walk(DIST);
if (bad.length) { console.error(`check:external FAILED: ${bad.length} third-party reference(s)\n` + [...new Set(bad)].join('\n')); process.exit(1); }
console.log('check:external ok: dist/ has no third-party URLs');
