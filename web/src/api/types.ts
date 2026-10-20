import type { components } from './schema';

/** Generated schema types, re-exported under short names. Never hand-edit shapes here. */
export type Schema<K extends keyof components['schemas']> = components['schemas'][K];

export type Me = Schema<'Me'>;
export type User = Schema<'User'>;
export type Permission = Schema<'Permission'>;
export type RoleKey = Schema<'Role'>['key'];
export type Role = Schema<'Role'>;
export type AuditEvent = Schema<'AuditEvent'>;
export type HardeningCheck = Schema<'HardeningCheck'>;
export type HardeningChange = Schema<'HardeningChange'>;
export type DomainRoute = Schema<'DomainRoute'>;
export type Project = Schema<'Project'>;
export type Resource = Schema<'Resource'>;
export type ResourceConfig = Schema<'ResourceConfig'>;
export type Deployment = Schema<'Deployment'>;
export type DeploymentStatus = Deployment['status'];
export type DeploymentLogLine = Schema<'DeploymentLogLine'>;
export type EnvVar = Schema<'EnvVar'>;
export type Volume = Schema<'Volume'>;
export type HostMetrics = Schema<'HostMetrics'>;
export type SystemStatus = Schema<'SystemStatus'>;
export type Module = Schema<'Module'>;
export type FeatureFlag = Schema<'FeatureFlag'>;
export type TerminalSession = Schema<'TerminalSession'>;
export type WebhookDelivery = Schema<'WebhookDelivery'>;
export type ResourceSource = Schema<'ResourceSource'>;
export type EventType = Schema<'EventEnvelope'>['type'];

export const DEPLOYMENT_STATES: DeploymentStatus[] = [
  'queued', 'preparing', 'building', 'starting', 'verifying', 'switching', 'observing',
  'succeeded', 'failed', 'cancelled', 'interrupted', 'rolled_back',
];
/** States in which the deployment can still be cancelled (plan 3.8: not after `switching`). */
export const CANCELLABLE: DeploymentStatus[] = ['queued', 'preparing', 'building', 'starting', 'verifying'];
export const IN_PROGRESS: DeploymentStatus[] = [...CANCELLABLE, 'switching', 'observing'];
export const TERMINAL: DeploymentStatus[] = ['succeeded', 'failed', 'cancelled', 'interrupted', 'rolled_back'];
