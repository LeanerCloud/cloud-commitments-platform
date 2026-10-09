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

### 1. Re-apply each cloud's `ci-cd-permissions`, keeping the old trust

The old name-keyed subjects keep working until step 2 switches the OIDC `sub` template. Apply the
ID-keyed trust first, next to them, so no deploy fails between the two steps. Run this for `aws`,
`azure` and `gcp`:

```bash
cd terraform/environments/<cloud>/ci-cd-permissions
# In terraform.tfvars: delete the old github_repo line and set
#   github_repository_id       = "1391259743"
#   github_repository_owner_id = "86753534"
# aws and azure only, for the cutover window:
#   legacy_github_repo         = "LeanerCloud/cloud-commitments-platform"   # the name the live trust uses
terraform init -backend-config=backend.hcl
terraform plan
terraform apply
```

Check that the plan contains only these changes. If it shows anything else, stop. Do not deploy
to any cloud while this step runs.

- **AWS**: `aws_iam_openid_connect_provider.github[0]` has moved to
  `aws_iam_openid_connect_provider.github`. The trust policy holds both subject shapes, about 1.9 KB
  of IAM's 2048-character limit, so one more environment in `role.tf` needs the legacy window to
  be closed first (or a split trust). `aws_iam_role.cudly_deploy` is updated in place: its
  `sub` list gains the `repository_owner_id:...` subjects and keeps the `repo:...` ones.
- **Azure**: `github_main[0]` has moved to `github_main`. The ID-keyed federated credentials
  change in place and `*-legacy` credentials keep the name-keyed subjects. Entra requires a unique
  issuer, subject and audience per application, so the `*-legacy` credentials `depends_on` the
  in-place updates and are created after them. The name-keyed Azure subject is absent for a few
  seconds in between. The application has 4 + 4 credentials, below Entra's limit of 20.
- **GCP**: the pool and provider move from `[0]` to unindexed addresses. The provider's
  `attribute_mapping` and `attribute_condition` are updated in place.
  `google_service_account_iam_member.github_actions` is replaced, because its `member` changes
  from `attribute.repository/<name>` to `attribute.repository_id/<id>`. GCP needs no legacy window:
  `repository_id` is a standard claim in every token, whatever the `sub` template says. Check the
  two IDs against `gh api` before applying, because a wrong ID rejects every GCP deploy.

### 2. Configure GitHub (the cutover)

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
- sets the OIDC `sub` claim template. New jobs now present the ID-keyed subjects that step 1
  already trusts. Jobs that request a token after this point present the ID-keyed subject, which step 1 already trusts.

### 3. Test a deploy from `main`

```bash
gh workflow run deploy-aws-lambda.yml -R LeanerCloud/cloud-commitments-platform --ref main -f environment=dev
gh workflow run deploy-azure.yml      -R LeanerCloud/cloud-commitments-platform --ref main -f environment=dev
gh workflow run deploy-gcp.yml        -R LeanerCloud/cloud-commitments-platform --ref main -f environment=dev
```

Each run must get past its cloud login step. Then dispatch one of the workflows from any other
branch. GitHub must refuse the environment-bound job ("not allowed to deploy to dev due to
environment protection rules").

### 4. Remove the old trust

Delete `legacy_github_repo` from `terraform.tfvars` for `aws` and `azure`, then `terraform plan`
(it must show only the `repo:...` subjects and the `*-legacy` credentials being removed) and
`terraform apply`. After this, no trust names a repository.

## Rollback

Restore the GitHub template and the Terraform together. Either one alone leaves every deploy
failing at the cloud login step.

Before step 4 the legacy trust is still in place, so restoring the default template is the whole
rollback: deploys go back to the name-keyed subjects at once. Do it first:

1. Restore the default `sub` template:

   ```bash
   gh api -X PUT repos/LeanerCloud/cloud-commitments-platform/actions/oidc/customization/sub -F use_default=true
   ```

2. Only if you must also revert the Terraform (after step 4, set `legacy_github_repo` again and
   re-apply first, or the name-keyed subjects are not trusted). Before you apply the previous
   module code with `github_repo` set, move each resource back to
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
