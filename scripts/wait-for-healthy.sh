#!/usr/bin/env bash
# wait-for-healthy.sh URL
#
# Smoke gate for a just-deployed service. Polls URL/ready until it answers a
# 2xx status HEALTH_REQUIRED_STREAK times in a row. A not-ready or failed
# response inside the polling budget resets the streak instead of failing,
# because during a rollout a probe can hit a replica that is still
# initializing. If the budget (HEALTH_MAX_ATTEMPTS) runs out before the streak
# is reached, exits 1 and prints the last response (or curl error).
#
# /ready is the traffic-admission endpoint: it answers 503 until the replica it
# answers for has completed initialization, so the gate reflects the same
# contract the load balancer uses (#488). /health answers 200 even when
# degraded and is a liveness signal; phase 2 below reads exactly one check from
# it. No warm-up request is needed: the server initializes its database in the
# background at startup.
#
# Phase 2 (platform#67): once /ready is stable, GET URL/health?jwks=refresh and
# read checks.scheduled_auth_jwks.status. "degraded" means the scheduled-task
# OIDC JWKS endpoint was unreachable at warmup and the server's bounded
# re-probe did not clear it, so /api/scheduled/* would answer 401. It is
# retried up to HEALTH_JWKS_MAX_ATTEMPTS rounds, then fails. Any other readable
# verdict (healthy, disabled, pending, check absent on an older revision)
# passes at once. Only the LAST readable verdict decides: a /health that
# cannot be read never fails the gate by itself (::warning:: if no round was
# readable). Requires jq.
#
# Only 2xx counts as ready. A 3xx is NOT ready and is logged with its redirect
# target: an http:// URL in front of an HTTPS-only listener answers 301 on every
# path, and counting that as ready would make the gate pass without ever
# reaching the app (#647). Redirects are deliberately not followed; point the
# gate at the URL that serves the app instead. A non-2xx body is kept so a 503
# still prints the JSON check detail that explains which dependency is not
# ready.
#
# Env (defaults suit the deploy workflows; tests shrink them):
#   HEALTH_REQUIRED_STREAK   consecutive ready responses needed (3)
#   HEALTH_MAX_ATTEMPTS      polling rounds before failing (30)
#   HEALTH_INTERVAL_SECONDS  sleep between rounds, both phases (5)
#   HEALTH_JWKS_CHECK        0 skips phase 2 (1)
#   HEALTH_JWKS_MAX_ATTEMPTS phase 2 rounds before failing (8)

set -euo pipefail

URL="${1:?usage: wait-for-healthy.sh URL}"
REQUIRED_STREAK="${HEALTH_REQUIRED_STREAK-3}"
MAX_ATTEMPTS="${HEALTH_MAX_ATTEMPTS-30}"
INTERVAL="${HEALTH_INTERVAL_SECONDS:-5}"
JWKS_CHECK="${HEALTH_JWKS_CHECK-1}"
JWKS_MAX_ATTEMPTS="${HEALTH_JWKS_MAX_ATTEMPTS-8}"

# Fail loudly on bad limits: 0 or non-numeric would silently behave like 1, and
# attempts < streak can never pass. A set-but-empty value is rejected too.
for pair in "HEALTH_REQUIRED_STREAK=${REQUIRED_STREAK}" "HEALTH_MAX_ATTEMPTS=${MAX_ATTEMPTS}" "HEALTH_JWKS_MAX_ATTEMPTS=${JWKS_MAX_ATTEMPTS}"; do
  if [[ ! "${pair#*=}" =~ ^[1-9][0-9]*$ ]]; then
    echo "::error::${pair%%=*} must be a positive integer, got '${pair#*=}'" >&2
    exit 2
  fi
done
if ((MAX_ATTEMPTS < REQUIRED_STREAK)); then
  echo "::error::HEALTH_MAX_ATTEMPTS (${MAX_ATTEMPTS}) is below HEALTH_REQUIRED_STREAK (${REQUIRED_STREAK}); the check could never pass" >&2
  exit 2
fi

if [[ "$JWKS_CHECK" != "0" ]] && ! command -v jq > /dev/null; then
  echo "::error::jq is required for the scheduled_auth_jwks check; install it or set HEALTH_JWKS_CHECK=0" >&2
  exit 2
fi

streak=0
ready=0
last="no response"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

for ((attempt = 1; attempt <= MAX_ATTEMPTS; attempt++)); do
  # curl exits 0 for any HTTP status here (no -f), so the status is judged
  # below. stderr goes to its own file: curl prints its -w output even when the
  # request fails, and mixing the two would garble the error message.
  : > "$tmp/body"
  if meta=$(curl -sS --max-time 10 -o "$tmp/body" -w '%{http_code} %{redirect_url}' "${URL}/ready" 2> "$tmp/err"); then
    code="${meta%% *}"
    redirect="${meta#* }"
    [[ "$redirect" == "$meta" ]] && redirect=""
    body="$(cat "$tmp/body")"
    case "$code" in
      2??)
        streak=$((streak + 1))
        echo "Readiness check ${attempt}: ready (${streak}/${REQUIRED_STREAK})"
        if ((streak >= REQUIRED_STREAK)); then
          echo "Service is ready."
          ready=1
          break
        fi
        sleep "$INTERVAL"
        continue
        ;;
      3??)
        last="HTTP ${code} redirect to ${redirect:-<no Location header>}; redirects are not followed, so point the gate at the URL that serves the app"
        ;;
      *)
        last="HTTP ${code}: ${body:-<empty body>}"
        ;;
    esac
  else
    err="$(cat "$tmp/err")"
    last="${err:-no response}"
  fi
  streak=0
  echo "Readiness check ${attempt}: not ready yet, streak reset: ${last}"
  sleep "$INTERVAL"
done

if ((ready == 0)); then
  echo "::error::Service not ready ${REQUIRED_STREAK}x in a row after ${MAX_ATTEMPTS} attempts. Last response: ${last}"
  exit 1
fi

if [[ "$JWKS_CHECK" == "0" ]]; then
  echo "scheduled_auth_jwks check skipped (HEALTH_JWKS_CHECK=0)"
  exit 0
fi

# Phase 2. Every curl/jq runs inside an if or || so set -e cannot abort a round.
last_readable=""
last_reason=""
for ((round = 1; round <= JWKS_MAX_ATTEMPTS; round++)); do
  verdict="unreadable"
  reason=""
  : > "$tmp/h"
  if code="$(curl -sS --max-time 10 -o "$tmp/h" -w '%{http_code}' "${URL}/health?jwks=refresh" 2> "$tmp/herr")" && [[ "$code" == 2?? ]]; then
    if v="$(jq -r '(.checks.scheduled_auth_jwks.status? // "absent")' "$tmp/h" 2> /dev/null)" && [[ -n "$v" ]]; then
      verdict="$v"
      reason="$(jq -r '.checks.scheduled_auth_jwks.reason // ""' "$tmp/h" 2> /dev/null)" || reason=""
    fi
  fi
  if [[ "$verdict" == "unreadable" ]]; then
    echo "JWKS check ${round}: /health not readable, ignoring this round"
  else
    last_readable="$verdict"
    last_reason="$reason"
    if [[ "$verdict" != "degraded" ]]; then
      echo "scheduled_auth_jwks ${verdict} (round ${round})"
      exit 0
    fi
    echo "JWKS check ${round}: scheduled_auth_jwks degraded (reason=${reason:-unknown})"
  fi
  if ((round < JWKS_MAX_ATTEMPTS)); then
    sleep "$INTERVAL"
  fi
done

if [[ "$last_readable" == "degraded" ]]; then
  echo "::error::scheduled_auth_jwks still degraded after ${JWKS_MAX_ATTEMPTS} rounds (reason=${last_reason:-unknown}); check SCHEDULED_TASK_OIDC_JWKS_URL and egress"
  exit 1
fi
echo "::warning::cannot read /health; scheduled_auth_jwks not verified"
exit 0
