#!/usr/bin/env bash
# wait-for-healthy.sh URL
#
# Smoke gate for a just-deployed service. Polls URL/health until it reports
# status "healthy" HEALTH_REQUIRED_STREAK times in a row. An unhealthy or
# failed response inside the polling budget resets the streak instead of
# failing, because during a rollout a probe can hit a replica that is still
# initializing. If the budget (HEALTH_MAX_ATTEMPTS) runs out before the streak
# is reached, exits 1 and prints the last response (or curl error).
#
# Each round first sends one request to HEALTH_WARMUP_PATH: /health reports but
# never triggers the lazy DB/auth-store init on the HTTP server; any /api/
# request does.
#
# Env (defaults suit the deploy workflows; tests shrink them):
#   HEALTH_REQUIRED_STREAK   consecutive healthy responses needed (3)
#   HEALTH_MAX_ATTEMPTS      polling rounds before failing (30)
#   HEALTH_INTERVAL_SECONDS  sleep between rounds (5)
#   HEALTH_WARMUP_PATH       path hit before each probe (/api/auth/check-admin)

set -euo pipefail

URL="${1:?usage: wait-for-healthy.sh URL}"
REQUIRED_STREAK="${HEALTH_REQUIRED_STREAK:-3}"
MAX_ATTEMPTS="${HEALTH_MAX_ATTEMPTS:-30}"
INTERVAL="${HEALTH_INTERVAL_SECONDS:-5}"
WARMUP_PATH="${HEALTH_WARMUP_PATH:-/api/auth/check-admin}"

streak=0
last="no response"

for ((attempt = 1; attempt <= MAX_ATTEMPTS; attempt++)); do
  curl -s -o /dev/null --max-time 30 "${URL}${WARMUP_PATH}" || true

  # /health returns HTTP 200 even when degraded; the body is the verdict.
  if body=$(curl -fsS --max-time 10 "${URL}/health" 2>&1); then
    last="$body"
    if jq -e '.status == "healthy"' >/dev/null 2>&1 <<<"$body"; then
      streak=$((streak + 1))
      echo "Health check ${attempt}: healthy (${streak}/${REQUIRED_STREAK})"
      if ((streak >= REQUIRED_STREAK)); then
        echo "All smoke tests passed!"
        exit 0
      fi
    else
      streak=0
      echo "Health check ${attempt}: not healthy yet, streak reset: ${body}"
    fi
  else
    last="${body:-no response}"
    streak=0
    echo "Health check ${attempt}: request failed, streak reset: ${last}"
  fi
  sleep "$INTERVAL"
done

echo "::error::Service not healthy ${REQUIRED_STREAK}x in a row after ${MAX_ATTEMPTS} attempts. Last response: ${last}"
exit 1
