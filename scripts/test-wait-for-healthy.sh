#!/usr/bin/env bash
# test-wait-for-healthy.sh
#
# Offline test for wait-for-healthy.sh: curl is stubbed via PATH. Also runs the
# pre-fix inline smoke loop (extracted verbatim from deploy-azure.yml) against
# the rollout-race scenario to show it fails there.
#
# Exits 0 when all cases pass; exits 1 on any failure.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SUT="${SCRIPT_DIR}/wait-for-healthy.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin"

# Stub curl. Requests to /api/ (warmup) succeed silently. Each /health request
# consumes the next token from $SCENARIO; the last token repeats forever.
# Tokens: H = healthy body, U = degraded body, R = connection refused.
cat > "$WORK/bin/curl" <<'STUB'
#!/usr/bin/env bash
for a in "$@"; do url="$a"; done
case "$url" in
  */api/*) exit 0 ;;
esac
n=$(cat "$STATE" 2>/dev/null || echo 0)
tokens=($SCENARIO)
i=$n
((i >= ${#tokens[@]})) && i=$((${#tokens[@]} - 1))
echo $((n + 1)) > "$STATE"
case "${tokens[$i]}" in
  H) echo '{"status":"healthy"}' ;;
  U) echo '{"status":"degraded","checks":{"auth_store":{"status":"unhealthy","message":"auth store not initialized"}}}' ;;
  R) echo "curl: (7) Failed to connect" >&2; exit 7 ;;
esac
STUB
chmod +x "$WORK/bin/curl"

pass=0
fail=0

# run_case LABEL EXPECTED_EXIT SCENARIO [OUTPUT_SUBSTRING]
run_case() {
  local label="$1" expected="$2" scenario="$3" needle="${4:-}"
  local out actual=0
  : > "$WORK/state"
  out=$(PATH="$WORK/bin:$PATH" STATE="$WORK/state" SCENARIO="$scenario" \
    HEALTH_INTERVAL_SECONDS=0 HEALTH_MAX_ATTEMPTS=8 HEALTH_REQUIRED_STREAK=3 \
    "$SUT" https://app.example 2>&1) || actual=$?
  if [[ "$actual" -ne "$expected" ]]; then
    echo "FAIL: $label (expected exit $expected, got $actual)"; echo "$out"
    ((fail++)) || true
  elif [[ -n "$needle" && "$out" != *"$needle"* ]]; then
    echo "FAIL: $label (output lacks '$needle')"; echo "$out"
    ((fail++)) || true
  else
    echo "PASS: $label"
    ((pass++)) || true
  fi
}

run_case "unhealthy then healthy x3 passes" 0 "U H H H"
run_case "old revision healthy once, then unhealthy, then healthy x3 passes" 0 "H U U H H H"
run_case "unhealthy in the middle of a streak resets it" 1 "H H U H H U" "auth store not initialized"
run_case "healthy once then unhealthy forever fails with last body" 1 "H U" "auth store not initialized"
run_case "connection refused forever fails with curl error" 1 "R" "Failed to connect"

run_case "curl error mid-streak resets it (H R H H U, streak 3, must not pass)" 1 "H R H H U" "Failed to connect"

# run_env_case LABEL EXPECTED_EXIT SCENARIO OUTPUT_SUBSTRING [ENV=VAL ...]
# Runs the SUT with ONLY the given env overrides (plus a zero sleep interval),
# so the script's own defaults (streak 3, 30 attempts) are exercised.
run_env_case() {
  local label="$1" expected="$2" scenario="$3" needle="$4"
  shift 4
  local out actual=0
  : > "$WORK/state"
  out=$(env PATH="$WORK/bin:$PATH" STATE="$WORK/state" SCENARIO="$scenario" \
    HEALTH_INTERVAL_SECONDS=0 "$@" "$SUT" https://app.example 2>&1) || actual=$?
  if [[ "$actual" -ne "$expected" ]]; then
    echo "FAIL: $label (expected exit $expected, got $actual)"; echo "$out"
    ((fail++)) || true
  elif [[ -n "$needle" && "$out" != *"$needle"* ]]; then
    echo "FAIL: $label (output lacks '$needle')"; echo "$out"
    ((fail++)) || true
  else
    echo "PASS: $label"
    ((pass++)) || true
  fi
}

run_env_case "defaults: H H U H H H passes only at attempt 6 (streak 3)" 0 "H H U H H H" "Health check 6: healthy (3/3)"
run_env_case "defaults: two healthy then unhealthy forever exhausts 30 attempts" 1 "H H U" "after 30 attempts"
run_env_case "streak 0 rejected" 2 "H" "HEALTH_REQUIRED_STREAK must be a positive integer" HEALTH_REQUIRED_STREAK=0
run_env_case "streak abc rejected" 2 "H" "HEALTH_REQUIRED_STREAK must be a positive integer" HEALTH_REQUIRED_STREAK=abc
run_env_case "streak empty rejected" 2 "H" "HEALTH_REQUIRED_STREAK must be a positive integer" HEALTH_REQUIRED_STREAK=
run_env_case "attempts 0 rejected" 2 "H" "HEALTH_MAX_ATTEMPTS must be a positive integer" HEALTH_MAX_ATTEMPTS=0
run_env_case "attempts abc rejected" 2 "H" "HEALTH_MAX_ATTEMPTS must be a positive integer" HEALTH_MAX_ATTEMPTS=abc
run_env_case "attempts empty rejected" 2 "H" "HEALTH_MAX_ATTEMPTS must be a positive integer" HEALTH_MAX_ATTEMPTS=
run_env_case "attempts below streak rejected" 2 "H" "could never pass" HEALTH_MAX_ATTEMPTS=2 HEALTH_REQUIRED_STREAK=3

# Pre-fix logic, verbatim from deploy-azure.yml (URL var as the workflow set it).
old_inline() {
  local URL=https://app.example RESPONSE i
  for i in {1..3}; do
    if RESPONSE=$(curl -fsS --max-time 10 "$URL/health") &&
      jq -e '.status == "healthy"' > /dev/null <<< "$RESPONSE"; then
      echo "Health check $i: passed"
    else
      echo "Health check $i: failed: ${RESPONSE:-no response}"
      return 1
    fi
    sleep 0
  done
}
: > "$WORK/state"
if PATH="$WORK/bin:$PATH" STATE="$WORK/state" SCENARIO="H U H H" old_inline >/dev/null 2>&1; then
  echo "FAIL: pre-fix inline loop unexpectedly passed the rollout-race scenario"
  ((fail++)) || true
else
  echo "PASS: pre-fix inline loop fails the rollout-race scenario (H U H H), the bug being fixed"
  ((pass++)) || true
fi

echo "passed=$pass failed=$fail"
[[ "$fail" -eq 0 ]]
