import type { components } from '@/api/schema';

export type ErrorBody = components['schemas']['Error'];
export type ErrCode = ErrorBody['code'];

/** Thrown by mock handlers; converted to the contract's error envelope. */
export class HttpError extends Error {
  constructor(public status: number, public code: ErrCode, message: string, public details?: Record<string, unknown>, public headers?: Record<string, string>) {
    super(message);
  }
}
export const notFound = (what = 'resource') => new HttpError(404, 'not_found', `${what} not found`);
export const invalid = (fields: Record<string, string>, message = 'Validation failed') => new HttpError(422, 'validation_failed', message, { fields });
export const conflict = (message: string, details?: Record<string, unknown>) => new HttpError(409, 'conflict', message, details);
export const forbidden = (message = 'You do not have permission to do this') => new HttpError(403, 'forbidden', message);

const CROCK = '0123456789ABCDEFGHJKMNPQRSTVWXYZ';
let counter = 0;
/** ULID-shaped opaque ids (time prefix + random). */
export function ulid(prefix = ''): string {
  let t = Date.now();
  let time = '';
  for (let i = 0; i < 10; i++) {
    time = CROCK[t % 32] + time;
    t = Math.floor(t / 32);
  }
  let rand = '';
  counter = (counter + 1) % 1024;
  for (let i = 0; i < 6; i++) rand += CROCK[Math.floor(Math.random() * 32)];
  return `${prefix}${time}${rand}${CROCK[counter % 32]}`;
}

export const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

export function canonicalJson(v: unknown): string {
  if (v === null || typeof v !== 'object') return JSON.stringify(v);
  if (Array.isArray(v)) return `[${v.map(canonicalJson).join(',')}]`;
  const o = v as Record<string, unknown>;
  return `{${Object.keys(o).sort().map((k) => `${JSON.stringify(k)}:${canonicalJson(o[k])}`).join(',')}}`;
}

export async function sha256Hex(input: string): Promise<string> {
  const buf = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(input));
  return [...new Uint8Array(buf)].map((b) => b.toString(16).padStart(2, '0')).join('');
}

export function randomHex(bytes: number): string {
  const a = new Uint8Array(bytes);
  crypto.getRandomValues(a);
  return [...a].map((b) => b.toString(16).padStart(2, '0')).join('');
}
export const fakeDigest = () => `sha256:${randomHex(32)}`;
export const slugRe = /^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$/;
export const hostRe = /^(?=.{1,253}$)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/;
export const envKeyRe = /^[A-Za-z_][A-Za-z0-9_]*$/;
