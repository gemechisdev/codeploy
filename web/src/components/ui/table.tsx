import type { HTMLAttributes, TdHTMLAttributes, ThHTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

/** Tables scroll inside their own container so the page never scrolls sideways (down to 360 px). */
export const Table = ({ className, ...p }: HTMLAttributes<HTMLTableElement>) => (
  <div className="w-full overflow-x-auto"><table className={cn('w-full text-left text-xs', className)} {...p} /></div>
);
export const THead = (p: HTMLAttributes<HTMLTableSectionElement>) => <thead className="border-b border-subtle text-[10px] uppercase tracking-wider text-muted" {...p} />;
export const TBody = (p: HTMLAttributes<HTMLTableSectionElement>) => <tbody className="divide-y divide-subtle" {...p} />;
export const TR = ({ className, ...p }: HTMLAttributes<HTMLTableRowElement>) => <tr className={cn('hover:bg-surface-subtle/60', className)} {...p} />;
export const TH = ({ className, ...p }: ThHTMLAttributes<HTMLTableCellElement>) => <th scope="col" className={cn('px-3 py-2 font-semibold whitespace-nowrap', className)} {...p} />;
export const TD = ({ className, ...p }: TdHTMLAttributes<HTMLTableCellElement>) => <td className={cn('px-3 py-2.5 align-middle', className)} {...p} />;
