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
