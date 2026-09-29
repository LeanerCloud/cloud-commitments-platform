# Deploy trust bootstrap runbook

The CI/CD deploy identities in `terraform/environments/{aws,azure,gcp}/ci-cd-permissions/` trust
GitHub Actions tokens from this repository, matched on its immutable IDs rather than its
`owner/name`. A rename, or a new repository that reuses a freed name, neither gains nor loses
deploy rights. Only `main` and the defined deployment environments can deploy.

This is a bootstrap step (see "CI/CD IAM" in `CLAUDE.md`). A privileged human runs it by hand.
The deploy workflows never run it.

## What each cloud accepts

The repository's OIDC `sub` claim template is set to
`["repository_owner_id", "repository_id", "context"]`, so GitHub mints subjects of the form:

```text
repository_owner_id:<owner_id>:repository_id:<repo_id>:ref:refs/heads/main
repository_owner_id:<owner_id>:repository_id:<repo_id>:environment:<name>
```

| Cloud | Trust | Matches |
| --- | --- | --- |
| AWS | `cudly-terraform-deploy` role, `StringEquals` on `token.actions.githubusercontent.com:sub` | the main-branch subject and one environment subject per name listed in `role.tf` |
| Azure | `cudly-terraform-deploy` app, one federated credential per subject | the main-branch subject and one environment subject per `var.github_environments` entry |
| GCP | WIF provider `github-actions` | `attribute_condition`: `assertion.repository_owner_id == '<owner_id>' && assertion.repository_id == '<repo_id>' && assertion.ref == '<deploy_ref>'`; the deploy SA grants `roles/iam.workloadIdentityUser` to `attribute.repository_id/<repo_id>` |

An environment subject does not say which branch triggered the run. Each environment's
deployment branch policy, limited to `main`, is what keeps other branches out. The bootstrap
script sets that policy.

For `LeanerCloud/cloud-commitments-platform`, `gh api repos/LeanerCloud/cloud-commitments-platform
--jq '.id,.owner.id'` returns `1391259743` and `86753534`.

## Prerequisites

- `main` is protected (#154). Protection controls who can reach the `ref:refs/heads/main` subject.
- A `gh` login with admin rights on the repository, plus `jq`.
- For each cloud, the credentials listed under "Prerequisites" in that module's `README.md`.

## Apply order

### 0. Record the current state

The rollback steps below restore from these files.

```bash
gh api repos/LeanerCloud/cloud-commitments-platform/actions/oidc/customization/sub > oidc-sub.before.json
gh api repos/LeanerCloud/cloud-commitments-platform/environments > environments.before.json
```

### 1. Configure GitHub

```bash
scripts/bootstrap-github-deploy-config.sh            # dry run: prints each WOULD: line
scripts/bootstrap-github-deploy-config.sh --apply
scripts/bootstrap-github-deploy-config.sh            # expect only OK: lines
```

The script:

- creates every environment the workflows bind to (`dev`, `staging`, `prod`,
  `aws-fargate-*`, `aws-db-*`, `gcp-db-*` and `azure-db-*`, each for `dev`/`staging`/`prod`), with
  custom deployment branch policies that admit only `main`. It keeps existing required reviewers
  and wait timers;
- stops with an error if an environment admits any other branch or tag. Remove the extra policy in
  **Settings > Environments**, then re-run;
- sets the OIDC `sub` claim template.

The old name-based subjects stop matching from this step on. They already fail for this repository
after the split, so this step causes no new outage.

### 2. Re-apply each cloud's `ci-cd-permissions`

Run this for `aws`, `azure` and `gcp`:

```bash
cd terraform/environments/<cloud>/ci-cd-permissions
# In terraform.tfvars: delete the old github_repo line and set
#   github_repository_id       = "1391259743"
#   github_repository_owner_id = "86753534"
terraform init -backend-config=backend.hcl
terraform plan
terraform apply
```

Check that the plan contains only these changes. If it shows anything else, stop.

- **AWS**: `aws_iam_openid_connect_provider.github[0]` has moved to
  `aws_iam_openid_connect_provider.github`. `aws_iam_role.cudly_deploy` is updated in place, and
  its `sub` list changes from `repo:...` to `repository_owner_id:...` subjects.
- **Azure**: `github_main[0]` has moved to `github_main`. Each federated credential's `subject`
  and `description` change.
- **GCP**: the pool and provider move from `[0]` to unindexed addresses. The provider's
  `attribute_mapping` and `attribute_condition` are updated in place.
  `google_service_account_iam_member.github_actions` is replaced, because its `member` changes
  from `attribute.repository/<name>` to `attribute.repository_id/<id>`.

After the apply, no trust names a repository, so the entries for the old monorepo name are gone.

### 3. Test a deploy from `main`

```bash
gh workflow run deploy-aws-lambda.yml -R LeanerCloud/cloud-commitments-platform --ref main -f environment=dev
gh workflow run deploy-azure.yml      -R LeanerCloud/cloud-commitments-platform --ref main -f environment=dev
gh workflow run deploy-gcp.yml        -R LeanerCloud/cloud-commitments-platform --ref main -f environment=dev
```

Each run must get past its cloud login step. Then dispatch one of the workflows from any other
branch. GitHub must refuse the environment-bound job ("not allowed to deploy to dev due to
environment protection rules").

## Rollback

Restore the GitHub template and the Terraform together. Either one alone leaves every deploy
failing at the cloud login step.

1. Restore the default `sub` template:

   ```bash
   gh api -X PUT repos/LeanerCloud/cloud-commitments-platform/actions/oidc/customization/sub -F use_default=true
   ```

2. Before you apply the previous module code with `github_repo` set, move each resource back to
   its indexed address. If you skip this, Terraform destroys and recreates the resources. A
   deleted GCP pool keeps its ID reserved for 30 days, so the recreate fails.

   ```bash
   # aws
   terraform state mv aws_iam_openid_connect_provider.github 'aws_iam_openid_connect_provider.github[0]'
   # azure
   terraform state mv azuread_application_federated_identity_credential.github_main \
     'azuread_application_federated_identity_credential.github_main[0]'
   # gcp
   terraform state mv google_iam_workload_identity_pool.github 'google_iam_workload_identity_pool.github[0]'
   terraform state mv google_iam_workload_identity_pool_provider.github 'google_iam_workload_identity_pool_provider.github[0]'
   terraform state mv google_service_account_iam_member.github_actions 'google_service_account_iam_member.github_actions[0]'
   ```

   Then run `terraform plan` and `terraform apply` from the previous commit.

3. You can leave the environments and their `main`-only branch policies in place, because they
   only restrict. `environments.before.json` records what existed before step 1.
