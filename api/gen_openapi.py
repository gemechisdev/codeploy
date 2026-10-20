#!/usr/bin/env python3
"""Builds api/openapi.yaml (OpenAPI 3.1). Run: python3 gen_openapi.py > openapi.yaml

The YAML file is the single source of truth; this script only keeps the ~100
operations and schemas consistent. Every operation carries x-permission.
"""
import sys
import yaml

S = "string"
def ref(n): return {"$ref": f"#/components/schemas/{n}"}
def arr(x): return {"type": "array", "items": x}
def obj(props, required=None, extra=False):
    o = {"type": "object", "properties": props, "additionalProperties": extra}
    if required: o["required"] = required
    return o
def enum(*v): return {"type": "string", "enum": list(v)}
def nul(t): return {"oneOf": [t, {"type": "null"}]}
ts = {"type": "string", "format": "date-time"}
nts = nul(ts)
i = {"type": "integer"}
n = {"type": "number"}
b = {"type": "boolean"}
s = {"type": "string"}
ns = nul(s)
def page(item): return obj({"items": arr(ref(item)), "next_cursor": ns}, ["items", "next_cursor"])

DEPLOY_STATES = ["queued", "preparing", "building", "starting", "verifying", "switching",
                 "observing", "succeeded", "failed", "cancelled", "interrupted", "rolled_back"]
PERMS = ("resource:read resource:write resource:delete resource:control deploy:create deploy:cancel "
         "deploy:rollback env:read env:write domain:write project:write terminal:host terminal:root "
         "terminal:container hardening:read hardening:apply edge:read edge:reconcile git:write hook:manage "
         "audit:read users:manage tokens:manage modules:manage flags:manage system:read system:metrics "
         "system:cleanup notify:manage backup:manage backup:restore template:deploy").split()

schemas = {
 "Error": obj({"code": enum("unauthenticated", "forbidden", "step_up_required", "validation_failed", "conflict",
                            "not_found", "rate_limited", "module_disabled", "preflight_failed", "internal"),
               "message": s, "details": {"type": "object", "additionalProperties": True}}, ["code", "message"]),
 "Permission": enum(*PERMS),
 "Role": obj({"key": enum("owner", "admin", "deployer", "viewer"), "name": s, "builtin": b,
              "permissions": arr(ref("Permission"))}, ["key", "name", "builtin", "permissions"]),
 "User": obj({"id": s, "email": s, "role": enum("owner", "admin", "deployer", "viewer"), "totp_enabled": b,
              "disabled": b, "created_at": ts, "last_login_at": nts},
             ["id", "email", "role", "totp_enabled", "disabled", "created_at", "last_login_at"]),
 "Me": obj({"user": ref("User"), "role": enum("owner", "admin", "deployer", "viewer"),
            "permissions": arr(ref("Permission")), "step_up_until": nts, "csrf_token": s, "server_now": ts,
            "bootstrap": b}, ["user", "role", "permissions", "step_up_until", "csrf_token", "server_now", "bootstrap"]),
 "LoginRequest": obj({"email": s, "password": s}, ["email", "password"]),
 "LoginResponse": obj({"totp_required": b, "challenge": ns}, ["totp_required"]),
 "TotpVerifyRequest": obj({"challenge": s, "code": s}, ["challenge", "code"]),
 "StepUpRequest": obj({"code": s}, ["code"]),
 "StepUpResponse": obj({"step_up_until": ts}, ["step_up_until"]),
 "CreateUserRequest": obj({"email": s, "password": s, "role": enum("admin", "deployer", "viewer")}, ["email", "password", "role"]),
 "PatchUserRequest": obj({"role": enum("admin", "deployer", "viewer"), "disabled": b}),
 "ApiToken": obj({"id": s, "name": s, "prefix": s, "scopes": arr(ref("Permission")), "last_used_at": nts,
                  "expires_at": nts, "created_at": ts}, ["id", "name", "prefix", "scopes", "last_used_at", "expires_at", "created_at"]),
 "CreateTokenRequest": obj({"name": s, "scopes": arr(ref("Permission")), "expires_at": nts}, ["name", "scopes"]),
 "TokenCreated": obj({"token": ref("ApiToken"), "secret": {"type": "string", "description": "Shown exactly once."}}, ["token", "secret"]),
 "AuditEvent": obj({"id": s, "seq": i, "at": ts, "actor_type": enum("user", "system", "token"), "actor_id": s,
                    "actor_email": ns, "ip": s, "action": s, "target_type": s, "target_id": s,
                    "meta": {"type": "object", "additionalProperties": True}, "prev_hash": s, "hash": s},
                   ["id", "seq", "at", "actor_type", "actor_id", "ip", "action", "target_type", "target_id", "meta", "prev_hash", "hash"]),
 "AuditVerifyResult": obj({"valid": b, "verified_rows": i, "first_invalid_seq": nul(i)}, ["valid", "verified_rows"]),

 # setup
 "SetupState": obj({"setup_required": b, "token_valid": b, "step": enum("token", "admin", "totp", "domain", "operator", "audit", "done"),
                    "has_operator": b}, ["setup_required", "step", "has_operator"]),
 "SetupAdminRequest": obj({"token": s, "email": s, "password": s}, ["token", "email", "password"]),
 "TotpEnrollment": obj({"secret": s, "otpauth_uri": s}, ["secret", "otpauth_uri"]),
 "TotpCodeRequest": obj({"code": s}, ["code"]),
 "RecoveryCodes": obj({"recovery_codes": arr(s)}, ["recovery_codes"]),
 "VerifyDomainRequest": obj({"hostname": s}, ["hostname"]),
 "VerifyDomainResult": obj({"hostname": s, "resolves": b, "resolved_ips": arr(s), "host_ips": arr(s), "matches_host": b,
                            "detail": s}, ["hostname", "resolves", "matches_host", "detail"]),
 "PanelDomainRequest": obj({"hostname": s}, ["hostname"]),
 "PanelDomainResult": obj({"hostname": s, "cert_status": enum("issuing", "issued", "error"), "url": ns, "detail": s}, ["hostname", "cert_status"]),
 "OperatorCandidate": obj({"username": s, "uid": i, "sudo": b, "has_authorized_keys": b}, ["username", "uid", "sudo", "has_authorized_keys"]),
 "OperatorCandidates": obj({"items": arr(ref("OperatorCandidate"))}, ["items"]),
 "SetOperatorRequest": obj({"mode": enum("existing", "create"), "username": s, "public_key": s}, ["mode", "username"]),

 # hardening
 "PlanDiff": obj({"target": s, "before": s, "after": s}, ["target", "before", "after"]),
 "HardeningCheck": obj({"key": s, "tier": {"type": "integer", "enum": [0, 1, 2, 3]}, "name": s, "description": s,
                        "status": enum("pass", "warn", "fail", "skipped"),
                        "driver": enum("nftables", "sshd", "fail2ban", "crowdsec", "apt", "sysctl", "users", "docker", "network"),
                        "detect_detail": s, "plan_diff": nul(ref("PlanDiff")), "apply_enabled": b, "requires_ack": b,
                        "experimental": b, "apply_flag": ns},
                       ["key", "tier", "name", "description", "status", "driver", "detect_detail", "plan_diff", "apply_enabled", "requires_ack"]),
 "HardeningChecks": obj({"items": arr(ref("HardeningCheck")), "score": i, "last_run_at": nts}, ["items", "score", "last_run_at"]),
 "PlanRequest": obj({"check_keys": arr(s)}, ["check_keys"]),
 "HardeningPlan": obj({"items": arr(obj({"check_key": s, "plan_diff": nul(ref("PlanDiff")), "commands": arr(s)}, ["check_key", "plan_diff", "commands"]))}, ["items"]),
 "PreflightRequest": obj({"check_key": s}, ["check_key"]),
 "PreflightItem": obj({"key": s, "label": s, "ok": b, "detail": s}, ["key", "label", "ok", "detail"]),
 "PreflightResult": obj({"check_key": s, "ok": b, "items": arr(ref("PreflightItem"))}, ["check_key", "ok", "items"]),
 "ApplyRequest": obj({"check_key": s, "ack": nul(obj({"console_access": b}, ["console_access"]))}, ["check_key"]),
 "HardeningChange": obj({"id": s, "check_key": s, "check_name": s, "tier": {"type": "integer", "enum": [0, 1, 2, 3]},
                         "snapshot_path": s, "revert_unit": ns, "applied_at": ts, "confirm_deadline": nts,
                         "confirmed_at": nts, "reverted_at": nts,
                         "state": enum("applied", "pending_confirm", "confirmed", "reverting", "reverted")},
                        ["id", "check_key", "check_name", "tier", "snapshot_path", "applied_at", "confirm_deadline", "confirmed_at", "reverted_at", "state"]),
 "HardeningChanges": obj({"items": arr(ref("HardeningChange")), "server_now": ts}, ["items", "server_now"]),
 "HardeningRun": obj({"id": s, "mode": enum("audit", "plan", "apply"), "started_at": ts, "finished_at": nts, "actor_id": s, "score": i},
                     ["id", "mode", "started_at", "finished_at", "actor_id", "score"]),

 # edge
 "DomainRoute": obj({"id": s, "resource_id": ns, "resource_name": ns, "hostname": s, "is_panel": b, "force_https": b,
                     "redirect_www": b, "cert_status": enum("issuing", "issued", "error"), "cert_expires_at": nts,
                     "cert_error": ns, "issuer": s, "upstream": s, "created_at": ts},
                    ["id", "resource_id", "hostname", "is_panel", "force_https", "redirect_www", "cert_status", "cert_expires_at", "issuer", "upstream", "created_at"]),
 "CreateDomainRequest": obj({"hostname": s, "force_https": b, "redirect_www": b}, ["hostname"]),
 "EdgeStatus": obj({"caddy_running": b, "admin_socket": s, "config_hash": s, "last_reconciled_at": nts, "drift": b,
                    "route_count": i, "server_now": ts}, ["caddy_running", "admin_socket", "config_hash", "last_reconciled_at", "drift", "route_count"]),
 "Certificate": obj({"hostname": s, "issuer": s, "status": enum("issuing", "issued", "error"), "not_after": nts, "error": ns},
                    ["hostname", "issuer", "status", "not_after"]),

 # workloads
 "Project": obj({"id": s, "name": s, "slug": s, "description": s, "created_at": ts}, ["id", "name", "slug", "description", "created_at"]),
 "CreateProjectRequest": obj({"name": s, "slug": s, "description": s}, ["name", "slug"]),
 "Environment": obj({"id": s, "project_id": s, "name": s}, ["id", "project_id", "name"]),
 "CreateEnvironmentRequest": obj({"name": s}, ["name"]),
 "ProjectTree": obj({"project": ref("Project"), "environments": arr(obj({"environment": ref("Environment"), "resources": arr(ref("Resource"))}, ["environment", "resources"]))}, ["project", "environments"]),
 "ResourceConfig": obj({
     "image": obj({"mode": enum("pull", "build"), "ref": ns, "dockerfile": ns, "context": ns}, ["mode"]),
     "port": i,
     "healthcheck": obj({"path": s, "interval_s": i, "retries": i, "start_period_s": i, "timeout_s": i}, ["path", "interval_s", "retries", "start_period_s", "timeout_s"]),
     "limits": obj({"memory_mb": i, "cpus": n, "pids_limit": i}, ["memory_mb", "cpus", "pids_limit"]),
     "isolation": obj({"cap_add": arr(s), "user": ns, "trusted": b}, ["cap_add", "trusted"]),
     "restart": enum("unless-stopped", "always", "on-failure", "no"),
     "retain_images": i}, ["image", "port", "healthcheck", "limits", "isolation", "restart", "retain_images"]),
 "Resource": obj({"id": s, "environment_id": s, "project_id": s, "environment_name": s, "type": enum("app", "compose", "static", "database", "service"),
                  "name": s, "slug": s, "config": ref("ResourceConfig"),
                  "status": enum("running", "deploying", "stopped", "failed", "degraded"),
                  "desired_state": enum("running", "stopped"), "active_deployment_id": ns, "active_digest": ns,
                  "primary_domain": ns, "started_at": nts, "created_at": ts},
                 ["id", "environment_id", "project_id", "environment_name", "type", "name", "slug", "config", "status", "desired_state", "active_deployment_id", "active_digest", "created_at"]),
 "CreateResourceRequest": obj({"environment_id": s, "type": enum("app", "compose", "static"), "name": s, "config": ref("ResourceConfig"),
                               "source": nul(ref("ResourceSource")), "domain": ns, "deploy_now": b},
                              ["environment_id", "type", "name", "config"]),
 "CreateResourceResponse": obj({"resource": ref("Resource"), "deployment": nul(ref("Deployment"))}, ["resource", "deployment"]),
 "PatchResourceRequest": obj({"name": s, "config": ref("ResourceConfig")}),
 "PolicyControl": obj({"key": s, "label": s, "value": s, "default": s, "state": enum("ok", "warn", "violation"), "note": ns}, ["key", "label", "value", "default", "state"]),
 "PolicyResult": obj({"resource_id": s, "ok": b, "controls": arr(ref("PolicyControl")), "violations": arr(s)}, ["resource_id", "ok", "controls", "violations"]),
 "EnvVar": obj({"id": s, "key": s, "value": ns, "scope": enum("build", "runtime", "both"), "is_secret": b, "updated_at": ts},
               ["id", "key", "value", "scope", "is_secret", "updated_at"]),
 "EnvVarInput": obj({"key": s, "value": ns, "scope": enum("build", "runtime", "both"), "is_secret": b,
                     "keep_existing_secret": {"type": "boolean", "description": "True = leave an existing secret unchanged (value omitted)."}},
                    ["key", "scope", "is_secret"]),
 "PutEnvRequest": obj({"items": arr(ref("EnvVarInput"))}, ["items"]),
 "EnvList": obj({"items": arr(ref("EnvVar"))}, ["items"]),
 "Volume": obj({"id": s, "name": s, "mount_path": s, "host_path": s, "kind": s, "size_mb": nul(i)}, ["id", "name", "mount_path", "host_path", "kind"]),
 "CreateVolumeRequest": obj({"name": s, "mount_path": s}, ["name", "mount_path"]),
 "Deployment": obj({"id": s, "resource_id": s, "status": enum(*DEPLOY_STATES), "step": s,
                    "trigger": enum("manual", "webhook", "deploy_hook", "rollback"), "commit_sha": ns, "commit_message": ns, "branch": ns,
                    "image_ref": s, "image_digest": ns, "started_at": ts, "finished_at": nts, "heartbeat_at": nts,
                    "observe_until": nts, "rollback_of": ns, "error": ns, "rollback_reason": ns,
                    "preflight": nul(obj({"ok": b, "disk_free_gb": n, "disk_required_gb": n, "ram_ok": b, "detail": s}, ["ok", "detail"])),
                    "health_attempts": obj({"done": i, "max": i, "last_status": ns}, ["done", "max"]),
                    "retained": b, "server_now": ts},
                   ["id", "resource_id", "status", "step", "trigger", "image_ref", "image_digest", "started_at", "finished_at", "retained"]),
 "CreateDeploymentRequest": obj({"mode": enum("pull", "build"), "image_ref": ns, "branch": ns}),
 "DeploymentLogLine": obj({"seq": i, "at": ts, "stream": enum("stdout", "stderr", "system"), "line": s}, ["seq", "at", "stream", "line"]),
 "RollbackRequest": obj({"acknowledge_db_unchanged": b}),

 # terminal
 "TicketRequest": obj({"target": enum("host", "container"), "resource_id": ns, "as_root": b}, ["target"]),
 "TicketResponse": obj({"ticket": s, "expires_at": ts, "idle_timeout_s": i}, ["ticket", "expires_at", "idle_timeout_s"]),
 "TerminalSession": obj({"id": s, "user_email": s, "ip": s, "target": enum("host", "container"), "target_name": s, "as_user": s,
                         "is_root": b, "started_at": ts, "ended_at": nts, "recorded": b},
                        ["id", "user_email", "ip", "target", "target_name", "as_user", "is_root", "started_at", "ended_at", "recorded"]),

 # git
 "GitConnection": obj({"id": s, "kind": enum("deploy_key", "github_app"), "name": s, "created_at": ts}, ["id", "kind", "name", "created_at"]),
 "ResourceSource": obj({"kind": enum("public_git", "deploy_key_git", "registry"), "repo_url": ns, "branch": ns, "build_path": ns,
                        "dockerfile": ns, "path_filters": arr(s), "deploy_key_id": ns},
                       ["kind", "path_filters"]),
 "DeployKey": obj({"id": s, "title": s, "public_key": s, "fingerprint": s, "created_at": ts}, ["id", "title", "public_key", "fingerprint", "created_at"]),
 "DeployHook": obj({"id": s, "token_prefix": s, "last_used_at": nts, "created_at": ts, "image_policy": s}, ["id", "token_prefix", "last_used_at", "created_at"]),
 "DeployHookCreated": obj({"hook": ref("DeployHook"), "token": {"type": "string", "description": "Shown once. URL is <origin>/api/v1/hooks/deploy/<token>."}}, ["hook", "token"]),
 "WebhookInfo": obj({"configured": b, "secret_prefix": ns, "created_at": nts, "path": s}, ["configured", "path"]),
 "WebhookCreated": obj({"webhook": ref("WebhookInfo"), "secret": s}, ["webhook", "secret"]),
 "WebhookDelivery": obj({"id": s, "provider": s, "delivery_id": s, "received_at": ts, "event": s,
                         "result": enum("success", "ignored", "rejected"), "reason": ns, "commit_sha": ns, "resource_id": ns},
                        ["id", "provider", "delivery_id", "received_at", "event", "result"]),

 # system
 "HostMetrics": obj({"cpu_percent": n, "memory_used_mb": i, "memory_total_mb": i, "daemon_rss_mb": n, "disk_used_gb": n,
                     "disk_total_gb": n, "net_rx_kbps": n, "net_tx_kbps": n, "container_count": i, "at": ts},
                    ["cpu_percent", "memory_used_mb", "memory_total_mb", "daemon_rss_mb", "disk_used_gb", "disk_total_gb", "container_count", "at"]),
 "SystemStatus": obj({"hostname": s, "os": s, "arch": s, "cpus": i, "ram_total_mb": i, "support_tier": enum("experimental", "limited", "primary", "comfortable", "headroom"),
                      "uptime_s": i, "metrics": ref("HostMetrics"), "operator_configured": b, "operator_user": ns,
                      "security_score": i, "pending_changes": i, "active_alerts": i, "server_now": ts},
                     ["hostname", "os", "arch", "cpus", "ram_total_mb", "support_tier", "uptime_s", "metrics", "operator_configured", "security_score", "pending_changes", "server_now"]),
 "MetricsSeries": obj({"range": s, "points": arr(ref("HostMetrics"))}, ["range", "points"]),
 "SystemVersion": obj({"version": s, "channel": enum("stable", "edge"), "commit": s, "built_at": ts, "go_version": s, "db_schema": i}, ["version", "channel", "commit", "built_at"]),
 "CleanupResult": obj({"reclaimed_mb": i, "images_removed": i, "containers_removed": i, "build_cache_mb": i}, ["reclaimed_mb", "images_removed", "containers_removed"]),
 "Module": obj({"key": s, "name": s, "description": s, "enabled": b, "core": b, "version": s, "phase": s}, ["key", "name", "description", "enabled", "core", "version", "phase"]),
 "PatchModuleRequest": obj({"enabled": b}, ["enabled"]),
 "FeatureFlag": obj({"key": s, "description": s, "enabled": b, "release_gated": b}, ["key", "description", "enabled", "release_gated"]),
 "PatchFlagsRequest": obj({"flags": {"type": "object", "additionalProperties": b}}, ["flags"]),
 "Settings": obj({"values": {"type": "object", "additionalProperties": True}}, ["values"]),

 # ops
 "NotificationChannel": obj({"id": s, "kind": enum("telegram", "discord", "email", "webhook"), "name": s, "enabled": b, "destination_hint": s, "created_at": ts}, ["id", "kind", "name", "enabled", "destination_hint", "created_at"]),
 "CreateChannelRequest": obj({"kind": enum("telegram", "discord", "email", "webhook"), "name": s, "config": {"type": "object", "additionalProperties": s}}, ["kind", "name", "config"]),
 "PatchChannelRequest": obj({"name": s, "enabled": b}),
 "ChannelTestResult": obj({"ok": b, "detail": s}, ["ok", "detail"]),
 "AlertRule": obj({"id": s, "key": s, "title": s, "condition": s, "threshold": s, "cooldown_s": i, "enabled": b, "channel_ids": arr(s), "last_triggered_at": nts},
                  ["id", "key", "title", "condition", "threshold", "cooldown_s", "enabled", "channel_ids", "last_triggered_at"]),
 "CreateAlertRuleRequest": obj({"key": s, "threshold": s, "cooldown_s": i, "channel_ids": arr(s)}, ["key"]),
 "PatchAlertRuleRequest": obj({"enabled": b, "threshold": s, "cooldown_s": i, "channel_ids": arr(s)}),
 "BackupSchedule": obj({"id": s, "resource_id": s, "resource_name": s, "cron": s, "destination": enum("local", "s3"), "retention_days": i,
                        "last_run_at": nts, "last_run_status": nul(enum("success", "failed", "running"))},
                       ["id", "resource_id", "resource_name", "cron", "destination", "retention_days", "last_run_at", "last_run_status"]),
 "CreateBackupScheduleRequest": obj({"cron": s, "destination": enum("local", "s3"), "retention_days": i}, ["cron", "destination", "retention_days"]),
 "BackupRun": obj({"id": s, "schedule_id": s, "resource_name": s, "status": enum("success", "running", "failed"), "size_bytes": i,
                   "checksum_sha256": ns, "location": s, "started_at": ts, "finished_at": nts, "error": ns},
                  ["id", "schedule_id", "resource_name", "status", "size_bytes", "location", "started_at"]),
 "RestoreResult": obj({"ok": b, "pre_restore_snapshot": s}, ["ok", "pre_restore_snapshot"]),
 "Template": obj({"key": s, "name": s, "category": s, "description": s, "icon": s, "default_port": i, "recommended_memory_mb": i, "kind": enum("app", "compose", "database")},
                 ["key", "name", "category", "description", "default_port", "recommended_memory_mb", "kind"]),
 "DeployTemplateRequest": obj({"project_id": s, "environment": s, "name": s}, ["project_id", "environment", "name"]),

 # events (documentation schemas for SSE)
 "EventEnvelope": obj({"id": s, "type": enum("resource.updated", "deployment.updated", "deployment.log", "hardening.changed", "edge.changed",
                                              "audit.appended", "metrics.sample", "session.step_up", "module.changed", "flag.changed"),
                       "data": {"type": "object", "additionalProperties": True}}, ["id", "type", "data"]),
}

# ---- operations -----------------------------------------------------------
ops = []
def op(method, path, oid, perm, summary, *, req=None, res=None, status="200", params=None, sse=False,
       tag=None, step_up=False, public=False, query=None, errors=None, res_desc=None, ws=False):
    ops.append(dict(method=method, path=path, oid=oid, perm=perm, summary=summary, req=req, res=res, status=status,
                    params=params or [], sse=sse, tag=tag or path.strip("/").split("/")[0], step_up=step_up, public=public,
                    query=query or [], errors=errors or [], ws=ws))

def P(name): return {"name": name, "in": "path", "required": True, "schema": s}
def Q(name, schema=s, desc=None):
    q = {"name": name, "in": "query", "required": False, "schema": schema}
    if desc: q["description"] = desc
    return q
LIM = [Q("limit", i), Q("cursor")]

# setup
op("get", "/setup/state", "getSetupState", None, "Setup state", res="SetupState", public=True)
op("post", "/setup/admin", "createSetupAdmin", None, "Create owner (needs one-time token; works once)", req="SetupAdminRequest", res="User", status="201", public=True)
op("post", "/setup/totp/enroll", "enrollSetupTotp", None, "Begin TOTP enrollment", res="TotpEnrollment", public=True)
op("post", "/setup/totp/verify", "verifySetupTotp", None, "Verify TOTP, return recovery codes once", req="TotpCodeRequest", res="RecoveryCodes", public=True)
op("post", "/setup/verify-domain", "verifySetupDomain", None, "DNS check for the panel domain", req="VerifyDomainRequest", res="VerifyDomainResult", public=True)
op("post", "/setup/panel-domain", "setPanelDomain", None, "Add panel domain route; nothing switches until issuance succeeds", req="PanelDomainRequest", res="PanelDomainResult", public=True)
op("get", "/setup/operator-candidates", "listOperatorCandidates", None, "Existing sudo users", res="OperatorCandidates", public=True)
op("post", "/setup/operator", "setOperator", None, "Select or create operator identity", req="SetOperatorRequest", res="OperatorCandidate", public=True)
op("post", "/setup/complete", "completeSetup", None, "Finish the wizard; setup endpoints are disabled afterwards", public=True, status="204")
# auth
op("post", "/auth/login", "login", None, "Password login", req="LoginRequest", res="LoginResponse", public=True)
op("post", "/auth/totp/verify", "verifyTotp", None, "Complete login with TOTP", req="TotpVerifyRequest", res="Me", public=True)
op("post", "/auth/step-up", "stepUp", "session", "TOTP step-up", req="StepUpRequest", res="StepUpResponse")
op("post", "/auth/logout", "logout", "session", "Logout", status="204")
op("get", "/auth/me", "getMe", "session", "Current session", res="Me")
# users etc
op("get", "/users", "listUsers", "users:manage", "List users", res="UserPage")
op("post", "/users", "createUser", "users:manage", "Create user", req="CreateUserRequest", res="User", status="201", step_up=True)
op("patch", "/users/{id}", "patchUser", "users:manage", "Update user", req="PatchUserRequest", res="User", params=[P("id")], step_up=True)
op("get", "/tokens", "listTokens", "tokens:manage", "List API tokens", res="TokenPage")
op("post", "/tokens", "createToken", "tokens:manage", "Create token (secret returned once)", req="CreateTokenRequest", res="TokenCreated", status="201")
op("delete", "/tokens/{id}", "revokeToken", "tokens:manage", "Revoke token", params=[P("id")], status="204")
op("get", "/roles", "listRoles", "session", "Roles with permissions", res="RoleList")
op("get", "/permissions", "listPermissions", "session", "Permission registry", res="PermissionList")
# audit
op("get", "/audit", "listAudit", "audit:read", "Audit events", res="AuditPage", query=LIM + [Q("actor"), Q("action"), Q("from", ts), Q("to", ts)])
op("post", "/audit/verify", "verifyAudit", "audit:read", "Verify hash chain server-side (tamper-evident)", res="AuditVerifyResult")
# hardening
op("get", "/hardening/checks", "listHardeningChecks", "hardening:read", "Checks with status, score, apply_enabled", res="HardeningChecks")
op("post", "/hardening/audit", "runHardeningAudit", "hardening:read", "Read-only audit (zero mutations)", res="HardeningChecks")
op("post", "/hardening/plan", "planHardening", "hardening:read", "Plan diffs and commands", req="PlanRequest", res="HardeningPlan")
op("post", "/hardening/preflight", "preflightHardening", "hardening:read", "Preflight for a Tier 2/3 check", req="PreflightRequest", res="PreflightResult")
op("post", "/hardening/apply", "applyHardening", "hardening:apply", "Apply (Tier 1-3); step-up required", req="ApplyRequest", res="HardeningChange", step_up=True, errors=["409"])
op("post", "/hardening/confirm/{changeId}", "confirmHardening", "hardening:apply", "Confirm connectivity", params=[P("changeId")], res="HardeningChange")
op("post", "/hardening/revert/{changeId}", "revertHardening", "hardening:apply", "Revert now", params=[P("changeId")], res="HardeningChange")
op("get", "/hardening/runs", "listHardeningRuns", "hardening:read", "Run history", res="HardeningRunPage", query=LIM)
op("get", "/hardening/changes", "listHardeningChanges", "hardening:read", "Pending and recent changes (amendment 1)", res="HardeningChanges")
# edge
op("get", "/edge/status", "getEdgeStatus", "edge:read", "Caddy status", res="EdgeStatus")
op("post", "/edge/reconcile", "reconcileEdge", "edge:reconcile", "Manual reconcile", res="EdgeStatus")
op("get", "/edge/certificates", "listCertificates", "edge:read", "Certificates", res="CertificateList")
op("get", "/domains", "listDomains", "edge:read", "All domains including the panel domain", res="DomainPage", query=LIM, tag="edge")
op("get", "/resources/{id}/domains", "listResourceDomains", "resource:read", "Resource domains", params=[P("id")], res="DomainPage", tag="resources")
op("post", "/resources/{id}/domains", "createResourceDomain", "domain:write", "Add domain", params=[P("id")], req="CreateDomainRequest", res="DomainRoute", status="201", tag="resources")
op("delete", "/resources/{id}/domains/{domainId}", "deleteResourceDomain", "domain:write", "Remove domain", params=[P("id"), P("domainId")], status="204", tag="resources")
# projects
op("get", "/projects", "listProjects", "resource:read", "Projects", res="ProjectPage", query=LIM)
op("post", "/projects", "createProject", "project:write", "Create project", req="CreateProjectRequest", res="Project", status="201")
op("get", "/projects/{id}", "getProject", "resource:read", "Project tree (environments + resources)", params=[P("id")], res="ProjectTree")
op("get", "/projects/{id}/environments", "listEnvironments", "resource:read", "Environments", params=[P("id")], res="EnvironmentList", tag="projects")
op("post", "/projects/{id}/environments", "createEnvironment", "project:write", "Create environment", params=[P("id")], req="CreateEnvironmentRequest", res="Environment", status="201", tag="projects")
# resources
op("get", "/resources", "listResources", "resource:read", "Resources", res="ResourcePage", query=LIM + [Q("environment_id"), Q("q")])
op("post", "/resources", "createResource", "resource:write", "Create resource (stopped until first deployment unless deploy_now)", req="CreateResourceRequest", res="CreateResourceResponse", status="201")
op("get", "/resources/{id}", "getResource", "resource:read", "Resource", params=[P("id")], res="Resource")
op("patch", "/resources/{id}", "patchResource", "resource:write", "Update resource", params=[P("id")], req="PatchResourceRequest", res="Resource")
op("delete", "/resources/{id}", "deleteResource", "resource:delete", "Delete resource", params=[P("id")], status="204")
for act in ("start", "stop", "restart"):
    op("post", f"/resources/{{id}}/{act}", f"{act}Resource", "resource:control", f"{act.capitalize()} resource", params=[P("id")], res="Resource", tag="resources")
op("get", "/resources/{id}/policy", "getResourcePolicy", "resource:read", "Effective isolation policy (amendment 4)", params=[P("id")], res="PolicyResult", tag="resources")
op("get", "/resources/{id}/env", "listEnv", "env:read", "Env vars (secret values never returned)", params=[P("id")], res="EnvList", tag="resources")
op("put", "/resources/{id}/env", "putEnv", "env:write", "Replace env set; replacing a secret value needs step-up", params=[P("id")], req="PutEnvRequest", res="EnvList", tag="resources", step_up=True)
op("get", "/resources/{id}/volumes", "listVolumes", "resource:read", "Volumes", params=[P("id")], res="VolumeList", tag="resources")
op("post", "/resources/{id}/volumes", "createVolume", "resource:write", "Add volume", params=[P("id")], req="CreateVolumeRequest", res="Volume", status="201", tag="resources")
op("get", "/resources/{id}/logs", "streamResourceLogs", "resource:read", "Container logs (SSE)", params=[P("id")], sse=True, query=[Q("tail", i), Q("since", ts)], tag="resources")
# deployments
op("post", "/resources/{id}/deployments", "createDeployment", "deploy:create", "Trigger deployment", params=[P("id")], req="CreateDeploymentRequest", res="Deployment", status="202", tag="deployments", errors=["409"])
op("get", "/resources/{id}/deployments", "listDeployments", "resource:read", "Deployments", params=[P("id")], res="DeploymentPage", query=LIM, tag="deployments")
op("get", "/deployments/{id}", "getDeployment", "resource:read", "Deployment", params=[P("id")], res="Deployment")
op("get", "/deployments/{id}/logs", "streamDeploymentLogs", "resource:read", "Deployment logs (SSE)", params=[P("id")], sse=True, query=[Q("after", i)])
op("post", "/deployments/{id}/cancel", "cancelDeployment", "deploy:cancel", "Cancel", params=[P("id")], res="Deployment")
op("post", "/deployments/{id}/rollback", "rollbackDeployment", "deploy:rollback", "Rollback to retained digest (no step-up; DB not rolled back)", params=[P("id")], req="RollbackRequest", res="Deployment", status="202")
# terminal
op("post", "/terminal/ticket", "createTerminalTicket", "terminal:container", "Single-use WS ticket (<=30 s); root needs step-up", req="TicketRequest", res="TicketResponse", status="201", step_up=True, errors=["409"])
op("get", "/terminal/host", "connectHostTerminal", "terminal:host", "Host PTY WebSocket (?ticket=)", ws=True, query=[Q("ticket")])
op("get", "/terminal/resources/{id}", "connectContainerTerminal", "terminal:container", "Container exec WebSocket (?ticket=)", params=[P("id")], ws=True, query=[Q("ticket")])
op("get", "/terminal/sessions", "listTerminalSessions", "terminal:host", "Session history", res="TerminalSessionPage", query=LIM)
# git
op("get", "/git/connections", "listGitConnections", "git:write", "Git connections", res="GitConnectionList")
op("post", "/git/connections", "createGitConnection", "git:write", "Create connection", req="GitConnection", res="GitConnection", status="201")
op("get", "/resources/{id}/source", "getResourceSource", "resource:read", "Source", params=[P("id")], res="ResourceSource", tag="resources")
op("put", "/resources/{id}/source", "putResourceSource", "git:write", "Set source", params=[P("id")], req="ResourceSource", res="ResourceSource", tag="resources")
op("post", "/resources/{id}/deploy-key", "createDeployKey", "git:write", "Generate ed25519 deploy key (public half only)", params=[P("id")], res="DeployKey", status="201", tag="resources")
op("get", "/resources/{id}/deploy-key", "getDeployKey", "resource:read", "Current deploy key", params=[P("id")], res="DeployKey", tag="resources")
op("get", "/resources/{id}/deploy-hook", "getDeployHook", "hook:manage", "Deploy hook metadata (prefix only)", params=[P("id")], res="DeployHook", tag="resources")
op("post", "/resources/{id}/deploy-hook", "createDeployHook", "hook:manage", "Create hook (token once)", params=[P("id")], res="DeployHookCreated", status="201", tag="resources")
op("post", "/resources/{id}/deploy-hook/rotate", "rotateDeployHook", "hook:manage", "Rotate hook (token once)", params=[P("id")], res="DeployHookCreated", tag="resources")
op("get", "/resources/{id}/webhook", "getWebhook", "hook:manage", "Webhook info", params=[P("id")], res="WebhookInfo", tag="resources")
op("post", "/resources/{id}/webhook", "createWebhook", "hook:manage", "Create/rotate webhook (secret once)", params=[P("id")], res="WebhookCreated", status="201", tag="resources")
op("get", "/webhooks/deliveries", "listWebhookDeliveries", "hook:manage", "Delivery history", res="WebhookDeliveryPage", query=LIM + [Q("resource_id")])
# system
op("get", "/system/status", "getSystemStatus", "system:read", "Host status", res="SystemStatus")
op("get", "/system/metrics", "getSystemMetrics", "system:metrics", "Metrics series", res="MetricsSeries", query=[Q("range")])
op("post", "/system/cleanup", "runCleanup", "system:cleanup", "Docker cleanup", res="CleanupResult")
op("get", "/system/version", "getSystemVersion", "system:read", "Version", res="SystemVersion")
op("get", "/modules", "listModules", "session", "Modules", res="ModuleList")
op("patch", "/modules/{key}", "patchModule", "modules:manage", "Toggle module", params=[P("key")], req="PatchModuleRequest", res="Module")
op("get", "/feature-flags", "listFeatureFlags", "session", "Feature flags", res="FlagList")
op("patch", "/feature-flags", "patchFeatureFlags", "flags:manage", "Update flags", req="PatchFlagsRequest", res="FlagList")
op("get", "/settings", "getSettings", "session", "Settings", res="Settings")
op("patch", "/settings", "patchSettings", "flags:manage", "Update settings", req="Settings", res="Settings")
# events
op("get", "/events", "streamEvents", "session", "App-wide SSE stream", sse=True, tag="events")
# notifications
op("get", "/notifications/channels", "listChannels", "notify:manage", "Channels", res="ChannelList")
op("post", "/notifications/channels", "createChannel", "notify:manage", "Create channel", req="CreateChannelRequest", res="NotificationChannel", status="201")
op("patch", "/notifications/channels/{id}", "patchChannel", "notify:manage", "Update channel", params=[P("id")], req="PatchChannelRequest", res="NotificationChannel")
op("delete", "/notifications/channels/{id}", "deleteChannel", "notify:manage", "Delete channel", params=[P("id")], status="204")
op("post", "/notifications/channels/{id}/test", "testChannel", "notify:manage", "Send test (amendment 3)", params=[P("id")], res="ChannelTestResult")
op("get", "/alerts/rules", "listAlertRules", "notify:manage", "Alert rules", res="AlertRuleList", tag="notifications")
op("post", "/alerts/rules", "createAlertRule", "notify:manage", "Create rule", req="CreateAlertRuleRequest", res="AlertRule", status="201", tag="notifications")
op("patch", "/alerts/rules/{id}", "patchAlertRule", "notify:manage", "Update rule", params=[P("id")], req="PatchAlertRuleRequest", res="AlertRule", tag="notifications")
# backups
op("get", "/resources/{id}/backups", "listResourceBackups", "backup:manage", "Backup schedules", params=[P("id")], res="BackupScheduleList", tag="backups")
op("post", "/resources/{id}/backups", "createBackupSchedule", "backup:manage", "Create schedule", params=[P("id")], req="CreateBackupScheduleRequest", res="BackupSchedule", status="201", tag="backups")
op("get", "/backups/schedules", "listBackupSchedules", "backup:manage", "All schedules", res="BackupScheduleList")
op("get", "/backups/runs", "listBackupRuns", "backup:manage", "Backup runs", res="BackupRunPage", query=LIM)
op("post", "/backups/{id}/run", "runBackup", "backup:manage", "Run schedule now", params=[P("id")], res="BackupRun", status="202")
op("post", "/backups/{id}/restore", "restoreBackup", "backup:restore", "Restore (step-up; pre-restore snapshot)", params=[P("id")], res="RestoreResult", step_up=True)
# templates
op("get", "/templates", "listTemplates", "template:deploy", "Template catalog", res="TemplateList")
op("post", "/templates/{key}/deploy", "deployTemplate", "template:deploy", "Deploy template", params=[P("key")], req="DeployTemplateRequest", res="CreateResourceResponse", status="201")

# list wrappers
for nm, item in [("UserPage", "User"), ("TokenPage", "ApiToken"), ("AuditPage", "AuditEvent"), ("HardeningRunPage", "HardeningRun"),
                 ("DomainPage", "DomainRoute"), ("ProjectPage", "Project"), ("ResourcePage", "Resource"), ("DeploymentPage", "Deployment"),
                 ("TerminalSessionPage", "TerminalSession"), ("WebhookDeliveryPage", "WebhookDelivery"), ("BackupRunPage", "BackupRun")]:
    schemas[nm] = page(item)
for nm, item in [("RoleList", "Role"), ("CertificateList", "Certificate"), ("EnvironmentList", "Environment"), ("VolumeList", "Volume"),
                 ("GitConnectionList", "GitConnection"), ("ModuleList", "Module"), ("FlagList", "FeatureFlag"), ("ChannelList", "NotificationChannel"),
                 ("AlertRuleList", "AlertRule"), ("BackupScheduleList", "BackupSchedule"), ("TemplateList", "Template")]:
    schemas[nm] = obj({"items": arr(ref(item))}, ["items"])
schemas["PermissionList"] = obj({"items": arr(obj({"key": ref("Permission"), "description": s}, ["key", "description"]))}, ["items"])

def ERR(code, d):
    return {"$ref": f"#/components/responses/E{code}"}
ERR_DEFS = {
  "E401": ("unauthenticated", None), "E403": ("forbidden | step_up_required", None), "E404": ("not_found | module_disabled", None),
  "E409": ("conflict | preflight_failed (details.checks)", None), "E422": ("validation_failed (details.fields)", None),
  "E429": ("rate_limited (Retry-After header)", None),
}

def build_paths():
    paths = {}
    for o in ops:
        item = paths.setdefault(o["path"], {})
        params = list(o["params"]) + list(o["query"])
        d = {"operationId": o["oid"], "summary": o["summary"], "tags": [o["tag"]],
             "x-permission": o["perm"] if o["perm"] else "public"}
        if o["step_up"]:
            d["x-step-up"] = True
            d["description"] = "May respond `403 step_up_required`; the client opens the step-up dialog and retries once."
        if params: d["parameters"] = params
        if o["public"]: d["security"] = []
        if o["req"]:
            d["requestBody"] = {"required": True, "content": {"application/json": {"schema": ref(o["req"])}}}
        responses = {}
        if o["sse"]:
            sse_schema = ref("EventEnvelope") if o["oid"] == "streamEvents" else (ref("DeploymentLogLine") if o["oid"] == "streamDeploymentLogs" else s)
            responses["200"] = {"description": "Server-sent events (each `data:` line is JSON of this schema)", "content": {"text/event-stream": {"schema": sse_schema}}}
            if o["oid"] == "streamEvents":
                d["x-events"] = {k: v for k, v in EVENTS.items()}
        elif o["ws"]:
            responses["200"] = {"description": "Upgrades to WebSocket (HTTP 101 on the wire). Binary frames = PTY bytes; text frames = JSON control "
                                "(client: {type:resize,cols,rows}; server: {type:exit,code} | {type:error,message})."}
        elif o["res"]:
            responses[o["status"]] = {"description": "OK", "content": {"application/json": {"schema": ref(o["res"])}}}
        else:
            responses[o["status"]] = {"description": "No content" if o["status"] == "204" else "OK"}
        if not o["public"]:
            responses["401"] = ERR(401, "unauthenticated")
        responses["403"] = ERR(403, "forbidden | step_up_required")
        responses["404"] = ERR(404, "not_found | module_disabled")
        if o["req"]: responses["422"] = ERR(422, "validation_failed (details.fields)")
        if "409" in o["errors"] or o["method"] in ("post", "put"): responses["409"] = ERR(409, "conflict | preflight_failed (details.checks)")
        if o["path"].startswith(("/auth", "/hooks", "/webhooks")) or o["public"]: responses["429"] = ERR(429, "rate_limited (Retry-After)")
        d["responses"] = responses
        item[o["method"]] = d
    return paths

EVENTS = {
 "resource.updated": {"resource_id": "string"},
 "deployment.updated": {"deployment_id": "string", "resource_id": "string", "status": "DeploymentStatus", "step": "string"},
 "deployment.log": {"deployment_id": "string", "seq": "integer", "at": "date-time", "stream": "stdout|stderr|system", "line": "string"},
 "hardening.changed": {"change_id": "string?"},
 "edge.changed": {"domain_id": "string?"},
 "audit.appended": {"seq": "integer"},
 "metrics.sample": "HostMetrics",
 "session.step_up": {"step_up_until": "date-time"},
 "module.changed": {"key": "string"},
 "flag.changed": {"key": "string"},
}

# the plan's webhook / hook endpoints: documented as server-side only
def server_side_only():
    return {
     "/webhooks/github": {"post": {"operationId": "receiveGithubWebhook", "tags": ["git"], "summary": "Server-side only (HMAC-SHA256 + delivery-ID dedupe). Not called by the UI.",
                                   "x-permission": "public", "security": [], "requestBody": {"content": {"application/json": {"schema": {"type": "object"}}}},
                                   "responses": {"202": {"description": "Accepted"}, "401": ERR(401, "bad signature"), "429": ERR(429, "rate_limited")}}},
     "/hooks/deploy/{token}": {"post": {"operationId": "receiveDeployHook", "tags": ["git"], "summary": "Server-side only (token in path, hashed at rest). Not called by the UI.",
                                        "x-permission": "public", "security": [], "parameters": [P("token")],
                                        "requestBody": {"content": {"application/json": {"schema": obj({"image": s, "digest": s}, ["image"])}}},
                                        "responses": {"202": {"description": "Accepted"}, "401": ERR(401, "bad token"), "429": ERR(429, "rate_limited")}}},
    }

spec = {
 "openapi": "3.1.0",
 "info": {"title": "codeplo API", "version": "1.2.0",
          "description": "OpenAPI-first contract for codeplod. JSON, snake_case, ISO-8601 UTC timestamps, opaque ULID ids. "
                         "Includes the v1.2 amendments listed in docs/CONTRACT.md.",
          "license": {"name": "Apache-2.0", "identifier": "Apache-2.0"}},
 "servers": [{"url": "/api/v1"}],
 "security": [{"cookieAuth": [], "csrfHeader": []}],
 "tags": [],
 "paths": build_paths(),
 "components": {
   "securitySchemes": {
     "cookieAuth": {"type": "apiKey", "in": "cookie", "name": "__Host-codeplo",
                    "description": "HttpOnly session cookie. Loopback bootstrap listener uses `codeplo_session` (plan 3.12)."},
     "csrfHeader": {"type": "apiKey", "in": "header", "name": "X-CSRF-Token",
                    "description": "Required on every non-GET request; value from GET /auth/me."}},
   "schemas": schemas},
}
spec["paths"].update(server_side_only())
spec["tags"] = [{"name": t, "description": f"{t} operations"} for t in sorted({o["tag"] for o in ops} | {"git"})]
spec["components"]["responses"] = {k: {"description": v[0], "content": {"application/json": {"schema": ref("Error")}}} for k, v in ERR_DEFS.items()}

class D(yaml.SafeDumper):
    def ignore_aliases(self, data): return True

yaml.dump(spec, sys.stdout, Dumper=D, sort_keys=False, width=120, allow_unicode=True)
