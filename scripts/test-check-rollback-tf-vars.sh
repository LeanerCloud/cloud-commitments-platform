#!/usr/bin/env bash
# Self-test for check-rollback-tf-vars.sh: it must fail on a missing TF_VAR_
# or a missing rollback job, and pass when every deploy TF_VAR_ is passed.
set -euo pipefail

check="$(git rev-parse --show-toplevel)/scripts/check-rollback-tf-vars.sh"
scratch="$(mktemp -d "${TMPDIR:-/tmp}/rollback-tf-vars.XXXXXXXX")"
trap 'rm -rf "$scratch"' EXIT

write_fixture() { # $1 = extra env line for the rollback-gcp job, $2 = job name for gcp
  for d in aws-lambda aws-fargate gcp azure; do
    printf 'jobs:\n  deploy:\n    steps:\n      - env:\n          TF_VAR_admin_email: x\n          TF_VAR_alert_emails: y\n' >"$scratch/deploy-$d.yml"
  done
  {
    printf 'jobs:\n'
    for j in rollback-aws-lambda rollback-aws-fargate; do
      printf '  %s:\n    steps:\n      - env:\n          TF_VAR_admin_email: x\n          TF_VAR_alert_emails: y\n' "$j"
    done
    printf '  %s:\n    steps:\n      - env:\n          TF_VAR_admin_email: x\n%b' "$2" "$1"
    printf '  rollback-azure:\n    steps:\n      - env:\n          TF_VAR_admin_email: x\n          TF_VAR_alert_emails: y\n'
    printf '  summary:\n    steps:\n      - env:\n          TF_VAR_alert_emails: y\n'
  } >"$scratch/rollback.yml"
}

expect() { # $1 = want exit code, $2 = label
  local got=0
  bash "$check" "$scratch" >/dev/null 2>&1 || got=$?
  if [[ $got -ne $1 ]]; then
    echo "FAIL: $2: exit $got, want $1" >&2
    exit 1
  fi
}

write_fixture '          TF_VAR_alert_emails: y\n' rollback-gcp
expect 0 "complete"
write_fixture '' rollback-gcp
expect 1 "missing TF_VAR_alert_emails (a later job's copy must not count)"
write_fixture '          TF_VAR_alert_emails: y\n' rollback-gcp-renamed
expect 1 "missing job"
echo "check-rollback-tf-vars self-test passed"
