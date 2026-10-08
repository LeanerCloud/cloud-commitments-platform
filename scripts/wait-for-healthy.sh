#!/usr/bin/env bash
# wait-for-healthy.sh URL
#
# Smoke gate for a just-deployed service. Polls URL/ready until curl
# --fail-with-body accepts the response (any status below 400)
# HEALTH_REQUIRED_STREAK times in a row. A not-ready or failed response inside
# the polling budget resets the streak instead of failing, because during a
# rollout a probe can hit a replica that is still initializing. If the budget
# (HEALTH_MAX_ATTEMPTS) runs out before the streak is reached, exits 1 and
# prints the last response (or curl error).
#
# /ready is the traffic-admission endpoint: it answers 503 until the replica it
# answers for has completed initialization, so the gate reflects the same
# contract the load balancer uses (#488). /health answers 200 even when
# degraded and is a liveness signal only. No warm-up request is needed: the
# server initializes its database in the background at startup.
#
# Requires curl 7.76+ for --fail-with-body, so a 503 still prints the JSON
# check detail that explains which dependency is not ready. Do not add -f:
# curl rejects -f/--fail together with --fail-with-body.
#
# Env (defaults suit the deploy workflows; tests shrink them):
#   HEALTH_REQUIRED_STREAK   consecutive ready responses needed (3)
#   HEALTH_MAX_ATTEMPTS      polling rounds before failing (30)
#   HEALTH_INTERVAL_SECONDS  sleep between rounds (5)

set -euo pipefail

URL="${1:?usage: wait-for-healthy.sh URL}"
REQUIRED_STREAK="${HEALTH_REQUIRED_STREAK-3}"
MAX_ATTEMPTS="${HEALTH_MAX_ATTEMPTS-30}"
INTERVAL="${HEALTH_INTERVAL_SECONDS:-5}"

# Fail loudly on bad limits: 0 or non-numeric would silently behave like 1, and
# attempts < streak can never pass. A set-but-empty value is rejected too.
for pair in "HEALTH_REQUIRED_STREAK=${REQUIRED_STREAK}" "HEALTH_MAX_ATTEMPTS=${MAX_ATTEMPTS}"; do
  if [[ ! "${pair#*=}" =~ ^[1-9][0-9]*$ ]]; then
    echo "::error::${pair%%=*} must be a positive integer, got '${pair#*=}'" >&2
    exit 2
  fi
done
if ((MAX_ATTEMPTS < REQUIRED_STREAK)); then
  echo "::error::HEALTH_MAX_ATTEMPTS (${MAX_ATTEMPTS}) is below HEALTH_REQUIRED_STREAK (${REQUIRED_STREAK}); the check could never pass" >&2
  exit 2
fi

streak=0
last="no response"

for ((attempt = 1; attempt <= MAX_ATTEMPTS; attempt++)); do
  # /ready answers 503 until the replica has completed initialization;
  # --fail-with-body keeps the JSON check detail so a failure says WHICH
  # dependency is not ready.
  if body=$(curl -sS --fail-with-body --max-time 10 "${URL}/ready" 2>&1); then
    streak=$((streak + 1))
    echo "Readiness check ${attempt}: ready (${streak}/${REQUIRED_STREAK})"
    if ((streak >= REQUIRED_STREAK)); then
      echo "Service is ready."
      exit 0
    fi
  else
    last="${body:-no response}"
    streak=0
    echo "Readiness check ${attempt}: not ready yet, streak reset: ${last}"
  fi
  sleep "$INTERVAL"
done

echo "::error::Service not ready ${REQUIRED_STREAK}x in a row after ${MAX_ATTEMPTS} attempts. Last response: ${last}"
exit 1
