import * as api from '../api';
import type { ConfigResponse } from '../types';

// In-flight cache for /api/config so each modal open does not refetch (issue
// #130d). The promise is captured the first time getConfig() is requested
// and reused for the lifetime of the page; resetConfigCache() lets tests
// (and future "force reload" UI) clear it explicitly.
let cachedConfigPromise: Promise<ConfigResponse> | undefined;

function getCachedConfig(): Promise<ConfigResponse> {
  if (!cachedConfigPromise) {
    cachedConfigPromise = api.getConfig().catch(err => {
      // Don't poison the cache on a transient failure — let the next caller
      // retry. (If we cached the rejection forever, a single early flake
      // would break the AWS modal for the rest of the session.)
      cachedConfigPromise = undefined;
      throw err;
    });
  }
  return cachedConfigPromise;
}

/** Reset the cached /api/config response. Test-only / future "reload" hook. */
export function resetConfigCache(): void {
  cachedConfigPromise = undefined;
}

/**
 * Map an AWS partition identifier (`aws`, `aws-cn`, `aws-us-gov`) to the
 * ARN prefix used in IAM trust policies. Defaults to `aws` for an empty
 * or unknown value so the rendered snippet always parses (issue #130c).
 */
function awsPartitionForArn(partition: string | undefined): string {
  switch (partition) {
    case 'aws-cn':
    case 'aws-us-gov':
      return partition;
    default:
      return 'aws';
  }
}

/**
 * Render the IAM trust-policy JSON snippet into the AWS role-mode
 * section. The snippet interpolates the CUDly host AWS account ID and
 * the per-account External ID so operators can copy it straight into
 * the role's trust relationship (issue #19).
 *
 * Partition awareness (issue #130c): the ARN prefix is taken from the
 * backend-reported partition so AWS China (`aws-cn`) and GovCloud
 * (`aws-us-gov`) deployments render a usable snippet instead of a
 * silently-wrong `arn:aws:` ARN.
 *
 * Race short-circuit (issue #130a): the auth-mode select is sampled
 * before the awaited getConfig() call and rechecked after. If the user
 * switched away from `role_arn` while the config was loading we abort
 * the write so we never paint into a hidden field.
 *
 * Config caching (issue #130d): /api/config is fetched at most once per
 * page load via getCachedConfig(); each modal open reuses the cached
 * promise instead of re-firing the GET.
 */
export async function renderAwsTrustPolicy(): Promise<void> {
  const block = document.getElementById('account-aws-trust-policy');
  const hint = document.getElementById('account-aws-trust-policy-hint');
  if (!block) return;
  const externalID = (document.getElementById('account-aws-external-id') as HTMLInputElement | null)?.value ?? '';

  // Snapshot the auth mode at call time — we'll recheck after the await
  // to detect a race where the user switched to bastion / WIF / keys
  // mid-fetch (issue #130a).
  const authModeSnapshot = (document.getElementById('account-aws-auth-mode') as HTMLSelectElement | null)?.value;

  let sourceAccountID = '';
  let partition = 'aws';
  try {
    const cfg = await getCachedConfig();
    if (cfg.source_identity?.provider === 'aws') {
      sourceAccountID = cfg.source_identity.account_id ?? '';
      partition = awsPartitionForArn(cfg.source_identity.partition);
    }
  } catch {
    // Non-critical — fall through to the placeholder text below.
  }

  // If the user switched auth modes while we were waiting on the API,
  // bail out: the role_arn fields are now hidden and writing into them
  // would either be a wasted DOM mutation or (worse) flash stale data
  // if they switch back later (issue #130a).
  const authModeNow = (document.getElementById('account-aws-auth-mode') as HTMLSelectElement | null)?.value;
  if (authModeSnapshot !== undefined && authModeSnapshot !== authModeNow) {
    return;
  }

  if (!sourceAccountID) {
    block.textContent = '';
    if (hint) {
      hint.textContent =
        "CUDly can't determine its host AWS account ID from this deployment " +
        "(either CUDly isn't running on AWS, or the Lambda/Container role " +
        "lacks sts:GetCallerIdentity). Ask a CUDly admin for the account ID, " +
        "then build the trust policy manually with Principal=arn:" + partition +
        ":iam::<CUDly account ID>:root, Action=sts:AssumeRole, and a " +
        "StringEquals sts:ExternalId condition on the value above.";
    }
    return;
  }

  const policy = {
    Version: '2012-10-17',
    Statement: [
      {
        Effect: 'Allow',
        Principal: { AWS: `arn:${partition}:iam::${sourceAccountID}:root` },
        Action: 'sts:AssumeRole',
        Condition: {
          StringEquals: { 'sts:ExternalId': externalID },
        },
      },
    ],
  };
  block.textContent = JSON.stringify(policy, null, 2);
  if (hint) {
    // The :root principal trusts every IAM principal in the CUDly host
    // account — the AWS-documented SaaS baseline (issue #130b). It
    // relies on the host account being well-secured and on the
    // sts:ExternalId condition for confused-deputy protection. A
    // tighter "lock to CUDly's compute role only" toggle is tracked
    // upstream; until it ships, customers who need least-privilege
    // can replace `:root` with the specific Lambda execution role ARN
    // out-of-band.
    hint.textContent =
      "Attach this policy to the IAM role's trust relationship so CUDly can " +
      "assume it. The Principal is the CUDly host AWS account root — every " +
      "IAM principal in that account is trusted, so the sts:ExternalId " +
      "condition is what locks this role to your specific CUDly registration.";
  }
}

/**
 * Render the IAM trust-policy snippet for the bastion auth mode (issue
 * #129). Mirrors renderAwsTrustPolicy but the Principal is the *bastion*
 * account root (the AWS account whose credentials call sts:AssumeRole
 * into the customer's role), not the CUDly host account.
 *
 * Rerun whenever the operator toggles into bastion mode or picks a
 * different bastion in the dropdown. If no bastion is selected yet, the
 * snippet block is cleared and the hint asks the operator to pick one.
 *
 * Race short-circuit (mirrors #130a): if the auth mode changed while we
 * were waiting on listAccounts/getConfig, abort so we don't paint into
 * a now-hidden field.
 */
export async function renderAwsBastionTrustPolicy(): Promise<void> {
  const block = document.getElementById('account-aws-trust-policy-bastion');
  const hint = document.getElementById('account-aws-trust-policy-bastion-hint');
  if (!block) return;
  const externalID = (document.getElementById('account-aws-external-id-bastion') as HTMLInputElement | null)?.value ?? '';
  const bastionSelectVal = (document.getElementById('account-aws-bastion-id') as HTMLSelectElement | null)?.value ?? '';

  // Snapshot the auth mode at call time — recheck after the awaits to
  // detect a race where the user switched modes mid-fetch (#130a).
  const authModeSnapshot = (document.getElementById('account-aws-auth-mode') as HTMLSelectElement | null)?.value;

  if (!bastionSelectVal) {
    block.textContent = '';
    if (hint) {
      hint.textContent =
        'Pick a bastion account above to render the trust policy snippet. ' +
        "The Principal will be the bastion's AWS account root; the " +
        'sts:ExternalId condition uses the value displayed above.';
    }
    return;
  }

  let bastionAccountID = '';
  let partition = 'aws';
  try {
    const [accounts, cfg] = await Promise.all([
      api.listAccounts({ provider: 'aws' }),
      getCachedConfig(),
    ]);
    const bastion = accounts.find(a => a.id === bastionSelectVal);
    bastionAccountID = bastion?.external_id ?? '';
    if (cfg.source_identity?.provider === 'aws') {
      partition = awsPartitionForArn(cfg.source_identity.partition);
    }
  } catch {
    // Non-critical — fall through to the placeholder text below.
  }

  const authModeNow = (document.getElementById('account-aws-auth-mode') as HTMLSelectElement | null)?.value;
  if (authModeSnapshot !== undefined && authModeSnapshot !== authModeNow) {
    return;
  }

  if (!bastionAccountID) {
    block.textContent = '';
    if (hint) {
      hint.textContent =
        "CUDly couldn't determine the bastion account ID. Build the trust " +
        'policy manually with Principal=arn:' + partition +
        ':iam::<bastion account ID>:root, Action=sts:AssumeRole, and a ' +
        'StringEquals sts:ExternalId condition on the value above.';
    }
    return;
  }

  const policy = {
    Version: '2012-10-17',
    Statement: [
      {
        Effect: 'Allow',
        Principal: { AWS: `arn:${partition}:iam::${bastionAccountID}:root` },
        Action: 'sts:AssumeRole',
        Condition: {
          StringEquals: { 'sts:ExternalId': externalID },
        },
      },
    ],
  };
  block.textContent = JSON.stringify(policy, null, 2);
  if (hint) {
    hint.textContent =
      "Attach this policy to the target IAM role's trust relationship so " +
      'CUDly can assume it through the bastion. The Principal is the ' +
      'bastion account root; the sts:ExternalId condition locks the role ' +
      'to this specific CUDly registration.';
  }
}
