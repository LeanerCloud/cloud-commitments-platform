#!/usr/bin/env bash
# check-stuck-deploy-runs.sh
#
# Reads a JSON array of workflow runs on stdin (the output of
# `gh run list --json databaseId,workflowName,status,createdAt,url,headBranch`)
# and reports runs that have been queued or pending for longer than
# STUCK_AFTER_MINUTES (default 120).
#
# Why: a deploy run stuck in `queued` holds the job-level concurrency group
# (`<cloud>-tfstate-<env>`, cancel-in-progress: false) and every later deploy
# or rollback waits behind it (issue #708). A `waiting` run is deliberately
# NOT reported: that is a run waiting for a required environment approval.
#
# Exit codes: 0 nothing stuck, 1 at least one stuck run, 2 bad input.
# NOW_EPOCH overrides the clock (used by the self-test).

set -euo pipefail

threshold_minutes="${STUCK_AFTER_MINUTES:-120}"
now_epoch="${NOW_EPOCH:-$(date -u +%s)}"

if ! [[ "$threshold_minutes" =~ ^[0-9]+$ ]]; then
  echo "STUCK_AFTER_MINUTES must be a non-negative integer, got '$threshold_minutes'" >&2
  exit 2
fi

stuck="$(jq -r --argjson now "$now_epoch" --argjson limit "$threshold_minutes" '
  map(select(.status == "queued" or .status == "pending"))
  | map(. + {age_min: (($now - (.createdAt | fromdateiso8601)) / 60 | floor)})
  | map(select(.age_min >= $limit))
  | sort_by(.createdAt)
  | .[]
  | "\(.workflowName) run \(.databaseId) (\(.headBranch)) has been \(.status) for \(.age_min) min: \(.url)"
' 2>/dev/null)" || {
  echo "input is not a JSON array of runs" >&2
  exit 2
}

if [[ -z "$stuck" ]]; then
  echo "No deploy run has been queued or pending for ${threshold_minutes} min or more."
  exit 0
fi

echo "$stuck"
echo
echo "Stuck queued runs hold the deploy concurrency group and block later deploys and rollbacks."
echo "Cancel them with: gh run cancel <run-id> --repo \"\$GITHUB_REPOSITORY\", then re-run the latest deploy."
exit 1
