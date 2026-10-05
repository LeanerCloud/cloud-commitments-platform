#!/usr/bin/env bash
# force-delete-owned-ecr-repo.sh
#
# Force-deletes the one ECR repository a Terraform state owns, and nothing else.
#
# Usage: force-delete-owned-ecr-repo.sh TERRAFORM_STATE_DIR
#
# Run before `terraform destroy` on a state that created an ECR repository: the
# repository is created with force_delete = false
# (terraform/modules/registry/aws/main.tf), so the destroy fails while images
# remain in it.
#
# The state directory is a required argument rather than a constant because it
# is the identity of what gets deleted. Every caller happens to pass
# terraform/environments/aws today, but which state the name is read from is
# the whole safety property here, so it stays visible at each call site.
#
# The owned name is read from `terraform output` on the state the destroy is
# about to tear down, looked up by that name alone, and the response compared by
# exact equality against it by scripts/select-owned-name.sh. The callers used to
# select by the `cudly-dev*` / `cudly-staging*` prefix, which also matches
# `cudly-staging-prod-mirror` and `cudly-staging-<hex>-backup` and force-deleted
# every image in them (#1592, #1820). The prefix also spans both staging states:
# cleanup-staging.yml's lambda and fargate jobs each create their own
# `cudly-staging-<random_id.suffix.hex>` repository (main.tf:55), so either job
# deleted the other's. The selector script holds the comparison and the name
# table pinning it, including why no prefix describes the owned repository
# uniquely.
#
# Reads the name through `output -json`, not `output -raw`: on a state with no
# outputs at all, `output -raw <name>` exits 0 and writes its "No outputs found"
# warning to STDOUT, so the name would become that warning text and re-running
# the cleanup after a completed one would fail here before `terraform destroy`
# ever ran. `output -json` returns `{}` for that state and the two cases
# separate cleanly:
#   no outputs at all        -> already destroyed, skip the ECR cleanup
#   outputs but not this one -> `jq -e` exits 1, this script fails loudly
#
# Nothing is swallowed. The `2>/dev/null || echo "may already be gone"` the
# callers used to carry reported success after a failed listing or a failed
# delete, and a failed listing is indistinguishable from an empty account, so
# the cleanup did nothing and `terraform destroy` then failed on the images
# still in the repository. "Already gone" is the one lookup error that is
# recognised, by name (RepositoryNotFoundException), and reported as a normal
# exit; every other error fails the script with the AWS CLI's own exit code.
#
# Exit codes:
#   0  cleanup completed, including the "state already destroyed" and "nothing
#      to delete" cases, which are normal outcomes and not errors
#   2  usage error (wrong arity, or a state directory that does not exist)
#   *  anything the AWS CLI, terraform, jq or the selector fails with, unmasked

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ $# -ne 1 ]]; then
  echo "usage: $(basename "$0") TERRAFORM_STATE_DIR" >&2
  exit 2
fi

STATE_DIR="$1"

if [[ ! -d "$STATE_DIR" ]]; then
  echo "error: terraform state directory '${STATE_DIR}' does not exist" >&2
  exit 2
fi

OUTPUTS_JSON="$(terraform -chdir="$STATE_DIR" output -json)"
# On its own line, not inlined into the `if` test: a jq failure inside
# `"$(jq ...)"` there would substitute an empty string, and `[[ "" -eq 0 ]]`
# evaluates true, treating a broken `terraform output` (non-JSON on stdout)
# the same as zero outputs -- a silent "already destroyed" exit. Assigned to
# its own variable, the failing command substitution's exit status is the
# assignment statement's own exit status, so `set -e` catches it here.
OUTPUTS_LENGTH="$(jq -r 'length' <<<"$OUTPUTS_JSON")"
if [[ "$OUTPUTS_LENGTH" -eq 0 ]]; then
  echo "State has no outputs; the stack is already destroyed and there is no ECR repository to clean up."
  exit 0
fi

OWNED_REPO="$(jq -er '.ecr_repository_name.value' <<<"$OUTPUTS_JSON")"
# An empty string is neither null nor false, so `jq -er` above accepts it.
# Whitespace is refused too: no repository name contains any.
case "$OWNED_REPO" in
  '' | *[![:graph:]]*)
    echo "error: state '${STATE_DIR}' publishes 'ecr_repository_name', but it resolved" >&2
    echo "       to '${OWNED_REPO}', which is not a repository name. Inspect" >&2
    echo "       'terraform -chdir=${STATE_DIR} output -json' before destroying anything." >&2
    exit 1
    ;;
esac

echo "This state owns ECR repository '$OWNED_REPO'"

# Asks for the owned repository by name rather than listing the account. A
# DescribeRepositories without --repository-names is authorized against
# repository/*, and the deploy role's ECR grant covers only repository/cudly-*
# (terraform/environments/aws/ci-cd-permissions/policy_compute.tf,
# ECRRepositoryScoped), so the listing was denied before the selector ever ran
# (#483). The response still goes through the selector, so "only the owned
# repository is ever deleted" stays a property of this script rather than of
# the API's filtering.
DESCRIBE_ERR="$(mktemp)"
trap 'rm -f "$DESCRIBE_ERR"' EXIT
DESCRIBE_EXIT=0
LISTING="$(aws ecr describe-repositories --repository-names "$OWNED_REPO" \
  --query 'repositories[].repositoryName' --output text 2>"$DESCRIBE_ERR")" || DESCRIBE_EXIT=$?
if [[ "$DESCRIBE_EXIT" -ne 0 ]]; then
  if grep -q '(RepositoryNotFoundException)' "$DESCRIBE_ERR"; then
    echo "ECR repository '$OWNED_REPO' does not exist; it is already deleted and there is nothing to clean up."
    exit 0
  fi
  cat "$DESCRIBE_ERR" >&2
  exit "$DESCRIBE_EXIT"
fi

printf '%s\n' "$LISTING" \
  | tr '\t' '\n' \
  | "${SCRIPT_DIR}/select-owned-name.sh" "$OWNED_REPO" \
  | while IFS= read -r REPO; do
      echo "Force-deleting ECR repo $REPO..."
      aws ecr delete-repository --repository-name "$REPO" --force
    done
