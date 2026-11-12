#!/usr/bin/env bash
# check-lambda-migrate-result.sh INVOKE_RESPONSE_FILE PAYLOAD_FILE
#
# Gate for the deploy-time `{"action":"migrate"}` Lambda invoke (#31). Takes the
# JSON `aws lambda invoke` prints on stdout (INVOKE_RESPONSE_FILE) and the
# payload file it wrote (PAYLOAD_FILE). `aws lambda invoke` exits 0 even when the
# function failed, so the failure only shows as a FunctionError key in the CLI
# response. Passes only when:
#
#   1. the CLI response is valid JSON and has no FunctionError key, and
#   2. the payload is valid JSON with .status == "ok".
#
# On success prints one line, "migrations ok: from <N> to <M>", for
# $GITHUB_STEP_SUMMARY. Every failure prints the reason to stderr and exits 1;
# a usage error exits 2.

set -uo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 INVOKE_RESPONSE_FILE PAYLOAD_FILE" >&2
  exit 2
fi

RESPONSE_FILE="$1"
PAYLOAD_FILE="$2"

fail() {
  echo "migrate invoke failed: $*" >&2
  exit 1
}

# The payload can carry a Go error with DB host or driver text and this output
# lands in the public job log, so show at most 500 bytes of it.
excerpt() {
  jq -c '.' "$PAYLOAD_FILE" | head -c 500
}

for f in "$RESPONSE_FILE" "$PAYLOAD_FILE"; do
  [[ -f "$f" ]] || fail "file '$f' does not exist"
  [[ -s "$f" ]] || fail "file '$f' is empty"
  jq -e 'type == "object"' "$f" >/dev/null 2>&1 || fail "file '$f' is not a JSON object"
done

if jq -e 'has("FunctionError")' "$RESPONSE_FILE" >/dev/null; then
  err=$(jq -r '.FunctionError' "$RESPONSE_FILE")
  fail "Lambda reported FunctionError '$err': $(excerpt)"
fi

status=$(jq -r 'if (.status | type) == "string" then .status else "" end' "$PAYLOAD_FILE")
[[ -n "$status" ]] || fail "payload has no string .status: $(excerpt)"
[[ "$status" == "ok" ]] || fail "payload .status is '$status', want 'ok': $(excerpt)"

echo "migrations ok: from $(jq -r '.from // "unknown"' "$PAYLOAD_FILE") to $(jq -r '.to // "unknown"' "$PAYLOAD_FILE")"
