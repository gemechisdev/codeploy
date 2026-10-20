import type { components } from '@/api/schema';

type EventType = components['schemas']['EventEnvelope']['type'];
export interface BusEvent {
  id: string;
  type: EventType;
  data: Record<string, unknown>;
}
type Sub = (e: BusEvent) => void;

/** In-process event bus feeding MockEventSource (`GET /events`). */
export class Bus {
  private subs = new Set<Sub>();
  private seq = 0;
  history: BusEvent[] = [];
  emit(type: EventType, data: Record<string, unknown>): BusEvent {
    const e: BusEvent = { id: String(++this.seq), type, data };
    this.history.push(e);
    if (this.history.length > 500) this.history.shift();
    this.subs.forEach((s) => s(e));
    return e;
  }
  subscribe(s: Sub) {
    this.subs.add(s);
    return () => this.subs.delete(s);
  }
  since(id: string): BusEvent[] {
    const n = Number(id);
    return Number.isFinite(n) ? this.history.filter((e) => Number(e.id) > n) : [];
  }
}
