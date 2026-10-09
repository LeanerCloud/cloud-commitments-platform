#!/usr/bin/env bash
# check-stuck-deploy-runs.sh
#
# Reads a JSON array of workflow runs on stdin (the output of
# `gh run list --json databaseId,workflowName,status,createdAt,url,headBranch`)
# and reports runs that have been queued or pending for STUCK_AFTER_MINUTES
# (default 120) or longer, and runs that have been waiting for
# WAITING_AFTER_MINUTES (default 720) or longer.
#
# Why: a deploy run stuck in `queued` holds the job-level concurrency group
# (`<cloud>-tfstate-<env>`, cancel-in-progress: false) and every later deploy
# or rollback waits behind it (issue #708). `waiting` is how an orphaned
# holder looked on AWS dev (no approval gate there), but it is also the state
# of a run legitimately waiting for a required environment approval, so it gets
# a much longer threshold and its own label.
#
# Exit codes: 0 nothing stuck, 1 at least one stuck run, 2 bad input.
# NOW_EPOCH overrides the clock (used by the self-test).

set -euo pipefail

threshold_minutes="${STUCK_AFTER_MINUTES:-120}"
waiting_minutes="${WAITING_AFTER_MINUTES:-720}"
now_epoch="${NOW_EPOCH:-$(date -u +%s)}"

for pair in "STUCK_AFTER_MINUTES=$threshold_minutes" "WAITING_AFTER_MINUTES=$waiting_minutes"; do
  if ! [[ "${pair#*=}" =~ ^[0-9]+$ ]]; then
    echo "${pair%%=*} must be a non-negative integer, got '${pair#*=}'" >&2
    exit 2
  fi
done

stuck="$(jq -r --argjson now "$now_epoch" --argjson limit "$threshold_minutes" --argjson wlimit "$waiting_minutes" '
  map(select(.status == "queued" or .status == "pending" or .status == "waiting"))
  | map(. + {age_min: (($now - (.createdAt | fromdateiso8601)) / 60 | floor)})
  | map(select(.age_min >= (if .status == "waiting" then $wlimit else $limit end)))
  | sort_by(.createdAt)
  | .[]
  | (if .status == "waiting" then "waiting (approval or orphaned)" else .status end) as $label
  | "\(.workflowName) run \(.databaseId) (\(.headBranch)) has been \($label) for \(.age_min) min: \(.url)"
' 2>/dev/null)" || {
  echo "input is not a JSON array of runs" >&2
  exit 2
}

if [[ -z "$stuck" ]]; then
  echo "No deploy run has been queued or pending for ${threshold_minutes} min or more, or waiting for ${waiting_minutes} min or more."
  exit 0
fi

echo "$stuck"
echo
echo "Stuck runs hold the deploy concurrency group and block later deploys and rollbacks."
echo "A waiting run may just be awaiting an environment approval; check the run page before cancelling."
echo "Cancel them with: gh run cancel <run-id> --repo \"\$GITHUB_REPOSITORY\", then re-run the latest deploy."
exit 1
