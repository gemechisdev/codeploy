// Grep guards from brief section 9.4. Runs as part of `npm run lint`.
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative, extname } from 'node:path';
import { fileURLToPath } from 'node:url';

// URL.pathname, not fileURLToPath, yields "/C:/..." on Windows and every fs call then fails on "C:\C:\...".
const SRC = fileURLToPath(new URL('../src', import.meta.url));
const files = [];
(function walk(d) { for (const n of readdirSync(d)) { const p = join(d, n); statSync(p).isDirectory() ? walk(p) : ['.ts', '.tsx'].includes(extname(p)) && files.push(p); } })(SRC);
// forward slashes on every host: path.relative yields "mock\server.ts" on Windows, and the prefix tests below
// are written with '/' because that is what the rule names and the source tree use.
const rel = (f) => relative(SRC, f).replaceAll('\\', '/');
const isTest = (f) => /__tests__|\.test\./.test(f);
const inMock = (f) => rel(f).startsWith('mock/');
const inApi = (f) => rel(f).startsWith('api/') && !rel(f).startsWith('api/schema');
const fails = [];
const rules = [
  ['no "reveal" for secrets', (f, s) => !isTest(f) && !inMock(f) && /\breveal(Secret|ed)?\b/i.test(s.replace(/\/\/.*$/gm, '').replace(/\/\*[\s\S]*?\*\//g, '')) && 'secrets are write-only: no reveal'],
  ['localStorage only for the theme key', (f, s) => !isTest(f) && !inMock(f) && [...s.matchAll(/localStorage\.\w+\(\s*([^,)]*)/g)].some((m) => !/THEME_KEY|codeplo_theme/.test(m[1])) && 'localStorage may only hold the theme'],
  ['no dangerouslySetInnerHTML', (f, s) => !isTest(f) && /dangerouslySetInnerHTML/.test(s) && 'dangerouslySetInnerHTML is forbidden'],
  ['no eval / new Function', (f, s) => !isTest(f) && /(^|[^.\w])eval\s*\(|new Function\s*\(/.test(s) && 'eval/new Function is forbidden (strict CSP)'],
  ['no fetch( outside src/api and src/mock', (f, s) => !isTest(f) && !inApi(f) && !inMock(f) && /(^|[^.\w])fetch\s*\(/.test(s) && 'use the typed API client, never raw fetch'],
  ['no appendAudit outside the mock server', (f, s) => !isTest(f) && !inMock(f) && /appendAudit/.test(s) && 'the client never writes audit rows'],
  ['no hard-coded emails/IPs outside mock', (f, s) => !isTest(f) && !inMock(f) && !inApi(f) && /[\w.-]+@[\w-]+\.(com|org|io|dev)\b|\b\d{1,3}(\.\d{1,3}){3}\b/.test(s.replace(/placeholder="[^"]*"/g, '')) && 'no hard-coded emails or IP addresses in UI code'],
  ['no prefilled demo credentials in UI', (f, s) => !isTest(f) && !inMock(f) && /123456|SecurePassw0rd|demo-password/.test(s) && 'no prefilled secrets in UI source'],
];
for (const f of files) {
  const s = readFileSync(f, 'utf8');
  for (const [name, test] of rules) { const r = test(f, s); if (r) fails.push(`${rel(f)}: ${name} (${r})`); }
}
if (fails.length) { console.error('lint-guards FAILED\n' + fails.join('\n')); process.exit(1); }
console.log(`lint-guards ok (${files.length} files, ${rules.length} rules)`);
