import type { DeploymentLogLine } from './types';

type Sub = (line: DeploymentLogLine) => void;
const subs = new Map<string, Set<Sub>>();

/** `deployment.log` events are appended to *open* log viewers only; they never touch the query cache. */
export const logBus = {
  subscribe(deploymentId: string, fn: Sub) {
    let s = subs.get(deploymentId);
    if (!s) subs.set(deploymentId, (s = new Set()));
    s.add(fn);
    return () => {
      s!.delete(fn);
      if (s!.size === 0) subs.delete(deploymentId);
    };
  },
  publish(deploymentId: string, line: DeploymentLogLine) {
    subs.get(deploymentId)?.forEach((f) => f(line));
  },
};
