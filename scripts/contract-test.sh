#!/usr/bin/env bash
# Contract run: frontend sequence replayed against a LIVE codeplod with curl.
# The operator step of the setup wizard needs a host sudo user (Linux /etc/passwd);
# on a non-Linux host the candidate list is empty, so it is exercised as a 422 and the
# backend's /setup/complete (which does not require an operator) is used to finish.
#
# Prereq: a live daemon with the setup token still valid (see docs/contract-test.md).
set -euo pipefail

B="${CODEPLO_BASE:-http://127.0.0.1:7443}/api/v1"
TOTP_SECRET_FILE="${CODEPLO_TOTP_SECRET:-/tmp/contract-secret.txt}"
JAR_SETUP="${CODEPLO_JAR_SETUP:-/tmp/contract-jar-setup.txt}"
JAR_LOGIN="${CODEPLO_JAR_LOGIN:-/tmp/contract-jar-login.txt}"
rm -f "$JAR_SETUP" "$JAR_LOGIN"

totp() { # $1 secret, $2 offset-steps(0)
  python3 - "$1" "${2:-0}" <<'PY'
import sys, time, hmac, hashlib
def b32d(s):
    s = s.upper().rstrip("="); A = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
    val = acc = 0; out = []
    for ch in s:
        val = (val << 5) | A.index(ch); acc += 5
        if acc >= 8:
            out.append((val >> (acc - 8)) & 0xFF); acc -= 8
    return bytes(out)
secret = sys.argv[1]; off = int(sys.argv[2])
key = b32d(secret); c = int(time.time()) // 30 + off
h = hmac.new(key, c.to_bytes(8, "big"), hashlib.sha1).digest(); o = h[19] & 0x0F
print(f"{(int.from_bytes(h[o:o+4], 'big') & 0x7FFFFFFF) % 1000000:06d}")
PY
}

# read the setup token from the daemon's first-run log
TOK="${CODEPLO_TOKEN:-$(grep -oE 'token=[a-f0-9]{64}' "${CODEPLO_DAEMON_LOG:-/tmp/ct-daemon.log}" | head -1 | cut -d= -f2)}"
echo "1. GET /setup/state (expect setup_required=true, step=token)"
curl -sS "$B/setup/state"; echo

echo "2. POST /setup/admin (create owner; 201, sets setup session cookie)"
curl -sS -c "$JAR_SETUP" -o /dev/null -w '%{http_code}\n' -X POST "$B/setup/admin" \
  -H 'Content-Type: application/json' \
  -d "{\"token\":\"$TOK\",\"email\":\"owner@example.com\",\"password\":\"correct horse battery\"}"

echo "3. POST /setup/totp/enroll (get the TOTP secret, shown once)"
ENR=$(curl -sS -b "$JAR_SETUP" -X POST "$B/setup/totp/enroll")
echo "$ENR"
SECRET=$(echo "$ENR" | sed -n 's/.*"secret":"\([^"]*\)".*/\1/p')
printf '%s' "$SECRET" > "$TOTP_SECRET_FILE"

echo "4. POST /setup/totp/verify (real authenticator code; returns 8 recovery codes)"
curl -sS -b "$JAR_SETUP" -X POST "$B/setup/totp/verify" -H 'Content-Type: application/json' \
  -d "{\"code\":\"$(totp "$SECRET")\"}"; echo

echo "5. GET /setup/operator-candidates (empty on a host without /etc/passwd)"
curl -sS -b "$JAR_SETUP" -o /dev/null -w '%{http_code}\n' "$B/setup/operator-candidates"

echo "6. POST /setup/operator mode=existing (expect 422: no sudo candidate here)"
curl -sS -b "$JAR_SETUP" -o /dev/null -w '%{http_code}\n' -X POST "$B/setup/operator" \
  -H 'Content-Type: application/json' -d '{"mode":"existing","username":"ubuntu"}' || true

echo "7. POST /setup/panel-domain (501 module_disabled until P2; skippable in UI)"
curl -sS -b "$JAR_SETUP" -o /dev/null -w '%{http_code}\n' -X POST "$B/setup/panel-domain" \
  -H 'Content-Type: application/json' -d '{"hostname":"panel.example.com"}' || true

echo "8. POST /setup/complete (204; backend does not require an operator)"
curl -sS -b "$JAR_SETUP" -o /dev/null -w '%{http_code}\n' -X POST "$B/setup/complete"

echo "9. POST /auth/login (totp_required + challenge)"
LG=$(curl -sS -c "$JAR_LOGIN" -X POST "$B/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"owner@example.com","password":"correct horse battery"}')
echo "$LG"
CH=$(echo "$LG" | sed -n 's/.*"challenge":"\([^"]*\)".*/\1/p')

echo "10. POST /auth/totp/verify (challenge + code -> Me, sets session + csrf)"
ME=$(curl -sS -b "$JAR_LOGIN" -c "$JAR_LOGIN" -X POST "$B/auth/totp/verify" \
  -H 'Content-Type: application/json' -d "{\"challenge\":\"$CH\",\"code\":\"$(totp "$SECRET")\"}")
echo "$ME" | head -c 200; echo
CSRF=$(echo "$ME" -n 1 2>/dev/null | head -c1 >/dev/null; echo "$ME" | sed -n 's/.*"csrf_token":"\([^"]*\)".*/\1/p')

echo "11. POST /users without step-up (expect 403 step_up_required)"
curl -sS -b "$JAR_LOGIN" -H "X-CSRF-Token: $CSRF" -o /dev/null -w '%{http_code}\n' \
  -X POST "$B/users" -H 'Content-Type: application/json' \
  -d '{"email":"dev@example.com","password":"another long passphrase","role":"deployer"}'

echo "12. POST /auth/step-up (fresh TOTP code; opens the window)"
# The login used the current TOTP step; the replay guard makes that code single-use,
# so the step-up must use a *fresh* code (the next 30 s step). This mirrors the real
# step-up dialog, where the user re-reads their authenticator.
curl -sS -b "$JAR_LOGIN" -H "X-CSRF-Token: $CSRF" -X POST "$B/auth/step-up" \
  -H 'Content-Type: application/json' -d "{\"code\":\"$(totp "$SECRET" 1)\"}"; echo

echo "13. POST /users again (now 201)"
curl -sS -b "$JAR_LOGIN" -H "X-CSRF-Token: $CSRF" -o /dev/null -w '%{http_code}\n' \
  -X POST "$B/users" -H 'Content-Type: application/json' \
  -d '{"email":"dev@example.com","password":"another long passphrase","role":"deployer"}'

echo "14. POST /audit/verify (expect valid=true)"
curl -sS -b "$JAR_LOGIN" -H "X-CSRF-Token: $CSRF" -X POST "$B/audit/verify"; echo

echo "15. PATCH /settings (e.g. session_ttl_h; 200, audit action settings.updated)"
curl -sS -b "$JAR_LOGIN" -H "X-CSRF-Token: $CSRF" -o /dev/null -w '%{http_code}\n' \
  -X PATCH "$B/settings" -H 'Content-Type: application/json' -d '{"values":{"session_ttl_h":24}}'

echo "16. PATCH /feature-flags (e.g. deploy.compose; 200, audit action flag.changed)"
curl -sS -b "$JAR_LOGIN" -H "X-CSRF-Token: $CSRF" -o /dev/null -w '%{http_code}\n' \
  -X PATCH "$B/feature-flags" -H 'Content-Type: application/json' -d '{"flags":{"deploy.compose":true}}'

echo "17. GET /auth/logout (204)"
curl -sS -b "$JAR_LOGIN" -H "X-CSRF-Token: $CSRF" -o /dev/null -w '%{http_code}\n' \
  -X POST "$B/auth/logout"

echo "CONTRACT RUN COMPLETE"
