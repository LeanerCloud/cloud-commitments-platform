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
wedge="2026-10-09T00:00:00Z" # exactly 720 min before NOW
wold="2026-10-08T23:00:00Z"  # 780 min before NOW

run_case "empty list is clean" 0 "[]" "No deploy run"
run_case "old queued run is stuck" 1 "[$(run 1 queued "$old")]" "run 1 (main) has been queued for 240 min"
run_case "old pending run is stuck" 1 "[$(run 2 pending "$old")]" "has been pending"
run_case "fresh queued run is fine" 0 "[$(run 3 queued "$fresh")]"
run_case "run exactly at the threshold is stuck" 1 "[$(run 4 queued "$edge")]"
run_case "waiting run under the waiting threshold is fine" 0 "[$(run 5 waiting "$old")]"
run_case "waiting run past the waiting threshold is reported" 1 "[$(run 10 waiting "$wold")]" "waiting (approval or orphaned) for 780 min"
run_case "waiting run exactly at the waiting threshold is reported" 1 "[$(run 11 waiting "$wedge")]"
run_case "old in_progress run is ignored" 0 "[$(run 6 in_progress "$old")]"
run_case "one stuck among fine runs is reported" 1 "[$(run 7 queued "$fresh"),$(run 8 queued "$old"),$(run 9 waiting "$old")]" "run 8"
run_case "a waiting run does not use the short threshold" 0 "[$(run 12 waiting "$edge")]"
ghost="2026-10-07T12:00:00Z" # exactly 2880 min (48 h) before NOW
gold="2026-10-07T11:00:00Z"  # 2940 min before NOW
run_case "queued run past 48 h is a ghost, not a failure" 0 "[$(run 13 queued "$gold")]" "Ghost runs"
run_case "ghost run is listed with its id" 0 "[$(run 14 queued "$gold")]" "run 14 (main) has been queued for 2940 min"
run_case "pending run at exactly 48 h is a ghost" 0 "[$(run 15 pending "$ghost")]" "report to GitHub Support"
run_case "queued run just under 48 h still fails" 1 "[$(run 16 queued "2026-10-07T12:01:00Z")]" "run 16"
run_case "ghost does not mask a fresh stuck run" 1 "[$(run 17 queued "$gold"),$(run 18 queued "$old")]" "run 18"
run_case "waiting run past 48 h is not a ghost" 1 "[$(run 19 waiting "$gold")]" "waiting (approval or orphaned)"
run_case "stuck run is listed under its own heading" 1 "[$(run 20 queued "$gold"),$(run 21 queued "$old")]" "Stuck runs:"
run_case "not JSON is a bad-input error" 2 "this is not json"
run_case "JSON object instead of array is a bad-input error" 2 '{"a":1}'

out_exit=0
STUCK_AFTER_MINUTES=abc bash "$CHECK" </dev/null >/dev/null 2>&1 || out_exit=$?
if [[ "$out_exit" -eq 2 ]]; then echo "PASS: non-numeric threshold is rejected"; (( pass++ )) || true
else echo "FAIL: non-numeric threshold (got exit $out_exit)"; (( fail++ )) || true; fi

gexit=0
GHOST_AFTER_MINUTES=x bash "$CHECK" </dev/null >/dev/null 2>&1 || gexit=$?
if [[ "$gexit" -eq 2 ]]; then echo "PASS: invalid ghost threshold is rejected"; (( pass++ )) || true
else echo "FAIL: invalid ghost threshold (got exit $gexit)"; (( fail++ )) || true; fi

g2exit=0
GHOST_AFTER_MINUTES=0 bash "$CHECK" </dev/null >/dev/null 2>&1 || g2exit=$?
if [[ "$g2exit" -eq 2 ]]; then echo "PASS: ghost threshold not above the stuck threshold is rejected"; (( pass++ )) || true
else echo "FAIL: ghost threshold below stuck threshold (got exit $g2exit)"; (( fail++ )) || true; fi

wexit=0
WAITING_AFTER_MINUTES=-5 bash "$CHECK" </dev/null >/dev/null 2>&1 || wexit=$?
if [[ "$wexit" -eq 2 ]]; then echo "PASS: invalid waiting threshold is rejected"; (( pass++ )) || true
else echo "FAIL: invalid waiting threshold (got exit $wexit)"; (( fail++ )) || true; fi

echo "$pass passed, $fail failed"
[[ "$fail" -eq 0 ]]
