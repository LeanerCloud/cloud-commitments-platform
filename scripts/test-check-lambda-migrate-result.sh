#!/usr/bin/env bash
# test-check-lambda-migrate-result.sh
#
# Offline test for check-lambda-migrate-result.sh with canned `aws lambda invoke`
# output and payload files. SUT=<path> runs the test against another copy of the
# script (used to prove a removed check makes a case fail).
#
# Exits 0 when all cases pass; exits 1 on any failure.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SUT="${SUT:-${SCRIPT_DIR}/check-lambda-migrate-result.sh}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# shellcheck disable=SC2016 # $LATEST is literal CLI output
OK_CLI='{"StatusCode":200,"ExecutedVersion":"$LATEST"}'
# shellcheck disable=SC2016
FAIL_CLI='{"StatusCode":200,"FunctionError":"Unhandled","ExecutedVersion":"$LATEST"}'
OK_PAYLOAD='{"status":"ok","from":12,"to":15}'

failures=0

# run_case NAME WANT_EXIT WANT_TEXT CLI_CONTENT PAYLOAD_CONTENT
# CLI_CONTENT or PAYLOAD_CONTENT equal to MISSING means the file is not created.
# WANT_TEXT must appear in the combined output.
run_case() {
  local name="$1" want_exit="$2" want_text="$3" cli="$4" payload="$5"
  local d="$WORK/$name"
  mkdir -p "$d"
  [[ "$cli" == MISSING ]] || printf '%s' "$cli" >"$d/cli.json"
  [[ "$payload" == MISSING ]] || printf '%s' "$payload" >"$d/out.json"
  local out rc=0
  out=$(bash "$SUT" "$d/cli.json" "$d/out.json" 2>&1) || rc=$?
  if [[ "$rc" -ne "$want_exit" ]] || ! grep -qF -- "$want_text" <<<"$out"; then
    echo "FAIL $name: exit $rc (want $want_exit), want text '$want_text', output: $out"
    failures=$((failures + 1))
  else
    echo "ok   $name"
  fi
}

run_case ok 0 "migrations ok: from 12 to 15" "$OK_CLI" "$OK_PAYLOAD"
run_case function_error 1 "FunctionError 'Unhandled'" "$FAIL_CLI" "$OK_PAYLOAD"
run_case function_error_error_payload 1 "FunctionError" "$FAIL_CLI" '{"errorMessage":"boom","errorType":"errorString"}'
run_case payload_array 1 "not a JSON object" "$OK_CLI" '[{"status":"ok"}]'
run_case payload_string 1 "not a JSON object" "$OK_CLI" '"ok"'
run_case status_missing 1 "no string .status" "$OK_CLI" '{"from":1,"to":2}'
run_case status_not_ok 1 "status is 'error'" "$OK_CLI" '{"status":"error"}'
run_case status_not_string 1 "no string .status" "$OK_CLI" '{"status":true}'
run_case malformed_payload 1 "not a JSON object" "$OK_CLI" '{"status":'
run_case malformed_cli 1 "not a JSON object" '{"StatusCode":' "$OK_PAYLOAD"
run_case empty_cli 1 "is empty" "" "$OK_PAYLOAD"
run_case empty_payload 1 "is empty" "$OK_CLI" ""
run_case missing_payload 1 "does not exist" "$OK_CLI" MISSING
run_case missing_cli 1 "does not exist" MISSING "$OK_PAYLOAD"

# Long error payloads must be capped in the output and still fail.
long=$(head -c 5000 /dev/zero | tr '\0' 'x')
d="$WORK/long"
mkdir -p "$d"
printf '%s' "$FAIL_CLI" >"$d/cli.json"
printf '{"errorMessage":"%s"}' "$long" >"$d/out.json"
rc=0
out=$(bash "$SUT" "$d/cli.json" "$d/out.json" 2>&1) || rc=$?
if [[ "$rc" -ne 1 || "${#out}" -gt 700 ]]; then
  echo "FAIL long_error: exit $rc (want 1), output length ${#out} (want <= 700)"
  failures=$((failures + 1))
else
  echo "ok   long_error"
fi

rc=0
bash "$SUT" only-one-arg >/dev/null 2>&1 || rc=$?
if [[ "$rc" -ne 2 ]]; then
  echo "FAIL usage: exit $rc (want 2)"
  failures=$((failures + 1))
else
  echo "ok   usage"
fi

if [[ "$failures" -ne 0 ]]; then
  echo "$failures case(s) failed"
  exit 1
fi
echo "all cases passed"
