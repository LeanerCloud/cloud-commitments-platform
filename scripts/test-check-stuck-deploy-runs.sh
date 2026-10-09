#!/usr/bin/env bash
# test-check-stuck-deploy-runs.sh
#
# Self-test for check-stuck-deploy-runs.sh. Fixed clock, fixture run lists.
# Exits 0 when all cases pass; exits 1 on any failure.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK="${SCRIPT_DIR}/check-stuck-deploy-runs.sh"

# 2026-10-09T12:00:00Z
NOW=1791547200

pass=0
fail=0

run_case() {
  local label="$1" expected_exit="$2" input="$3" expected_text="${4:-}"
  local out actual_exit=0
  out="$(printf '%s' "$input" | NOW_EPOCH="$NOW" bash "$CHECK" 2>&1)" || actual_exit=$?
  if [[ "$actual_exit" -ne "$expected_exit" ]]; then
    echo "FAIL: $label (expected exit $expected_exit, got $actual_exit)"
    (( fail++ )) || true
    return
  fi
  if [[ -n "$expected_text" && "$out" != *"$expected_text"* ]]; then
    echo "FAIL: $label (output missing '$expected_text')"
    (( fail++ )) || true
    return
  fi
  echo "PASS: $label"
  (( pass++ )) || true
}

run() { # id status createdAt
  printf '{"databaseId":%s,"workflowName":"Deploy to GCP Cloud Run","status":"%s","createdAt":"%s","url":"https://example.invalid/runs/%s","headBranch":"main"}' "$1" "$2" "$3" "$1"
}

old="2026-10-09T08:00:00Z"   # 240 min before NOW
fresh="2026-10-09T11:30:00Z" # 30 min before NOW
edge="2026-10-09T10:00:00Z"  # exactly 120 min before NOW

run_case "empty list is clean" 0 "[]" "No deploy run"
run_case "old queued run is stuck" 1 "[$(run 1 queued "$old")]" "run 1 (main) has been queued for 240 min"
run_case "old pending run is stuck" 1 "[$(run 2 pending "$old")]" "has been pending"
run_case "fresh queued run is fine" 0 "[$(run 3 queued "$fresh")]"
run_case "run exactly at the threshold is stuck" 1 "[$(run 4 queued "$edge")]"
run_case "old waiting run (environment approval) is ignored" 0 "[$(run 5 waiting "$old")]"
run_case "old in_progress run is ignored" 0 "[$(run 6 in_progress "$old")]"
run_case "one stuck among fine runs is reported" 1 "[$(run 7 queued "$fresh"),$(run 8 queued "$old"),$(run 9 waiting "$old")]" "run 8"
run_case "not JSON is a bad-input error" 2 "this is not json"
run_case "JSON object instead of array is a bad-input error" 2 '{"a":1}'

out_exit=0
STUCK_AFTER_MINUTES=abc bash "$CHECK" </dev/null >/dev/null 2>&1 || out_exit=$?
if [[ "$out_exit" -eq 2 ]]; then echo "PASS: non-numeric threshold is rejected"; (( pass++ )) || true
else echo "FAIL: non-numeric threshold (got exit $out_exit)"; (( fail++ )) || true; fi

echo "$pass passed, $fail failed"
[[ "$fail" -eq 0 ]]
