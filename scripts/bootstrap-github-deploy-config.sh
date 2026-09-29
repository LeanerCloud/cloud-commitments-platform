#!/usr/bin/env bash
# Configures the GitHub side of the CI/CD deploy trust:
#   1. every deployment environment the workflows bind to, with a deployment
#      branch policy that admits only main;
#   2. the repository's OIDC sub claim template, which decides the subject the
#      terraform/environments/*/ci-cd-permissions trusts match.
#
# Dry-run by default; --apply performs the changes. Idempotent: settings that
# already match are left alone. Needs a gh login with admin on the repository.
# Apply order and rollback: docs/deploy-trust-bootstrap.md.
set -euo pipefail

REPO="LeanerCloud/cloud-commitments-platform"
DEPLOY_BRANCH="main"

# Must stay in lockstep with local.github_oidc_sub_prefix in
# terraform/environments/{aws,azure}/ci-cd-permissions; the Go test
# terraform/environments/deploy_trust_guard_test.go enforces it.
SUB_CLAIM_KEYS='["repository_owner_id","repository_id","context"]'

# Every environment a job in .github/workflows binds to, with the
# ${{ ... }} expressions resolved to their dev/staging/prod choices.
ENVIRONMENTS=(
  dev staging prod
  aws-fargate-dev aws-fargate-staging aws-fargate-prod
  aws-db-dev aws-db-staging aws-db-prod
  gcp-db-dev gcp-db-staging gcp-db-prod
  azure-db-dev azure-db-staging azure-db-prod
)

usage() {
  echo "Usage: $0 [--dry-run | --apply]"
  echo "  --dry-run  print what would change (default)"
  echo "  --apply    make the changes on github.com/$REPO"
}

APPLY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --apply) APPLY=1 ;;
    --dry-run) APPLY=0 ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
  shift
done

die() {
  echo "ERROR: $*" >&2
  exit 1
}

# change DESCRIPTION CMD...: runs CMD under --apply, only describes it otherwise.
change() {
  local desc="$1"
  shift
  if [ "$APPLY" -eq 1 ]; then
    echo "APPLY: $desc"
    "$@" >/dev/null
  else
    echo "WOULD: $desc"
  fi
}

command -v gh >/dev/null || die "gh CLI not found"
command -v jq >/dev/null || die "jq not found"

repo_ids=$(gh api "repos/$REPO" --jq '"repository_id=\(.id) repository_owner_id=\(.owner.id)"')
echo "Repository: $REPO ($repo_ids)"
echo "Check these IDs against terraform.tfvars in each ci-cd-permissions module."
[ "$APPLY" -eq 1 ] || echo "Dry run: nothing will be changed. Re-run with --apply to make the changes."

existing_envs=$(gh api --paginate "repos/$REPO/environments" --jq '.environments[]?.name')

for env in "${ENVIRONMENTS[@]}"; do
  current='{}'
  if printf '%s\n' "$existing_envs" | grep -qxF "$env"; then
    current=$(gh api "repos/$REPO/environments/$env")
  fi

  policies=''
  if jq -e '.deployment_branch_policy == {"protected_branches": false, "custom_branch_policies": true}' \
    <<<"$current" >/dev/null; then
    echo "OK:    environment $env uses custom branch policies"
    # This endpoint only answers once custom branch policies are enabled.
    policies=$(gh api --paginate "repos/$REPO/environments/$env/deployment-branch-policies" \
      --jq '.branch_policies[] | "\(.type // "branch"):\(.name)"')
  else
    # PUT replaces the environment's settings, so carry over any wait timer
    # and required reviewers it already has.
    payload=$(jq -c '
      (.protection_rules // []) as $r
      | {deployment_branch_policy: {protected_branches: false, custom_branch_policies: true}}
        + ([$r[] | select(.type == "wait_timer") | {wait_timer}] | add // {})
        + ([$r[] | select(.type == "required_reviewers")
            | {prevent_self_review, reviewers: [.reviewers[] | {type, id: .reviewer.id}]}] | add // {})
    ' <<<"$current")
    change "create/update environment $env with $payload" \
      gh api -X PUT "repos/$REPO/environments/$env" --input - <<<"$payload"
  fi

  others=$(printf '%s\n' "$policies" | grep -vxF "branch:$DEPLOY_BRANCH" | grep -v '^$' || true)
  [ -z "$others" ] || die "environment $env admits refs other than $DEPLOY_BRANCH:
$others
Remove them in Settings -> Environments -> $env, then re-run."

  if printf '%s\n' "$policies" | grep -qxF "branch:$DEPLOY_BRANCH"; then
    echo "OK:    environment $env admits branch $DEPLOY_BRANCH only"
  else
    change "limit environment $env to branch $DEPLOY_BRANCH" \
      gh api -X POST "repos/$REPO/environments/$env/deployment-branch-policies" \
      -f name="$DEPLOY_BRANCH" -f type=branch
  fi
done

sub_payload=$(jq -cn --argjson keys "$SUB_CLAIM_KEYS" '{use_default: false, include_claim_keys: $keys}')
current_sub=$(gh api "repos/$REPO/actions/oidc/customization/sub")
if jq -e --argjson want "$sub_payload" '. == $want' <<<"$current_sub" >/dev/null; then
  echo "OK:    OIDC sub claim template is $SUB_CLAIM_KEYS"
else
  change "set OIDC sub claim template to $sub_payload (currently $current_sub)" \
    gh api -X PUT "repos/$REPO/actions/oidc/customization/sub" --input - <<<"$sub_payload"
fi
