#!/usr/bin/env bash
# Each rollback job's Terraform apply must pass every TF_VAR_ its deploy
# workflow passes; a missing one resets that variable to its default on the
# live stack (issue #1744). Usage: check-rollback-tf-vars.sh [workflows dir]
set -euo pipefail

dir="${1:-$(git rev-parse --show-toplevel)/.github/workflows}"
pairs=(
  "rollback-aws-lambda deploy-aws-lambda.yml"
  "rollback-aws-fargate deploy-aws-fargate.yml"
  "rollback-gcp deploy-gcp.yml"
  "rollback-azure deploy-azure.yml"
)

# job_vars prints the TF_VAR_ names used inside one top-level job of a workflow.
job_vars() {
  awk -v job="  $2:" '
    $0 == job { in_job = 1; next }
    in_job && /^  [A-Za-z0-9_-]+:$/ { exit }
    in_job { print }
  ' "$1" | grep -o 'TF_VAR_[A-Za-z0-9_]*' | sort -u || true
}

status=0
for pair in "${pairs[@]}"; do
  read -r job deploy <<<"$pair"
  rollback_vars="$(job_vars "$dir/rollback.yml" "$job")"
  if [[ -z "$rollback_vars" ]]; then
    echo "FAIL: rollback.yml has no job $job or it sets no TF_VAR_" >&2
    status=1
    continue
  fi
  missing="$(comm -23 <(grep -o 'TF_VAR_[A-Za-z0-9_]*' "$dir/$deploy" | sort -u) <(printf '%s\n' "$rollback_vars"))"
  if [[ -n "$missing" ]]; then
    echo "FAIL: rollback.yml job $job does not pass $(echo $missing) from $deploy" >&2
    status=1
  fi
done
[[ $status -eq 0 ]] && echo "rollback jobs pass every deploy TF_VAR_"
exit $status
