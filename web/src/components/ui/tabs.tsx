import * as RT from '@radix-ui/react-tabs';
import { cn } from '@/lib/cn';

export const Tabs = RT.Root;
export const TabsList = ({ className, ...p }: RT.TabsListProps) => <RT.List className={cn('flex gap-1 overflow-x-auto border-b border-subtle', className)} {...p} />;
export const TabsTrigger = ({ className, ...p }: RT.TabsTriggerProps) => (
  <RT.Trigger className={cn('shrink-0 cursor-pointer border-b-2 border-transparent px-3 py-2 text-xs font-semibold text-secondary hover:text-primary data-[state=active]:border-brand-primary data-[state=active]:text-brand-primary focus-visible:outline-2 focus-visible:outline-brand-primary', className)} {...p} />
);
export const TabsContent = ({ className, ...p }: RT.TabsContentProps) => <RT.Content className={cn('pt-4 focus-visible:outline-none', className)} {...p} />;
