# API request adapter audit, 2026-11-09

| | |
| --- | --- |
| Audited commit | `72d90d54b06769710b7c49094365892d9a4d4ab0` |
| Commit is | the tip of `origin/main` at audit start, not the working tree |
| Audit window | 2026-11-09 |
| Adapters audited | 42 (request type, call path) pairs in eight areas, plus one non-adapter body with field notes |
| Decode sites listed | 64, every line the decode-site grep returns (appendix) |
| Verification | every row was derived by reading the code at the audited commit with `git show` and `git grep`; no row was exercised at runtime |

This audit answers issue #19: for each API request adapter, which request fields reach their target and which are accepted but dropped. An adapter is a (request type, handler call path) pair, because one request type can feed several paths with different outcomes: `CloudAccountRequest` goes through `cloudAccountFromRequest` on create, update, self-registration and approval, and `PlanRequest.target_accounts` is ignored only on update. A pair exists where a request's fields are copied into a different struct or passed as positional arguments to a service or store. A request decoded directly into a store or provider struct, or used for validation only, is not an adapter and is marked as such in the appendix.

## Method and limits

The scan started from the decode sites, not from type names: `git grep -n -E '(Unmarshal|Decode)\(' 72d90d54b06769710b7c49094365892d9a4d4ab0 -- internal/api internal/server ':!*_test.go'` returns 64 lines, and every one has a row in the appendix. Every request-shaped type (`git grep -n -E '^type .*(Request|Body) struct'`) was cross-checked against that list. Every row of every table was traced from the request struct to the converter, the service or store call and the SQL column or outbound call, at the audited commit only.

Each field row has one disposition:

- **reaches the target**: the value is used by the callee's store write or outbound call. It wins over the other labels when the value is also validated. A value passed through whole (a map, a list of tiers) gets one row marked "as a whole value".
- **validation or authorization only**: the value decides the outcome or gates the call and is not stored.
- **deliberately ignored**: the value is not used, and an existing source comment says so (cited).
- **DROPPED**: accepted by the decoder, neither stored nor forwarded nor used to decide the outcome on that path, and no comment says so. A field that is only logged counts as dropped.

What this audit does not prove:

- **Three-level trace.** Each field is traced through at most three call levels past the handler (converter, service, store method) to the SQL column or outbound SDK field. Where a trace ends earlier, the cell says so. Two cells use that escape: `price_schedule[].term_months` and `price_schedule[].price` in B7, whose outbound SDK field is inside the external module `cloud-commitments-go`. Cells that cite "pinned pkg" or "pinned azure provider" read that module at the version pinned in `go.mod` at the audited commit.
- **Converter guards do not prove the store write.** A reflection or converter test shows that the converter's output holds the field. It does not show that the store persists it, and a later handler can still overwrite the value (the approval path did, see R2). Cells that cite such a test say "converter only".
- **Decode sites only.** The appendix covers exactly the lines the grep returns. Structs built without a `Unmarshal` or `Decode` call (the CSV form of an override import, a request built in code, a query-string scope struct) are listed in the extra table of the appendix, not as grep rows.
- **Plain decoders accept unknown keys.** The decoders at `handler_ri_exchange.go:981`, `:1222`, `:1956`, `:2029`, `:2305`, `handler_purchases_revoke.go:296` and `handler_purchases.go:3233` use `json.Unmarshal`, so an unknown key is accepted and ignored. The resolve, direct-resolve and settle decoders reject unknown keys. An unknown key is not a field of the request struct, so it has no row.
- **Point in time.** The tables describe the audited commit. Later commits changed some behavior; where a finding was fixed after the audited commit the table says so and the Findings section names the change.

## Summary

| Area | Pairs | DROPPED rows at the audited commit | Status |
| --- | --- | --- | --- |
| Accounts | 5 (A1 to A5) | 0 | matches |
| Registrations | 3 (R1 to R3) | 1 (R2 `enabled`) | fixed in #1905 |
| Plans | 6 (P1, B1 to B5) | 5 (`description` on create and update, `target_accounts` on update, service-config `provider` and `service`) | `description` and `target_accounts` open in #1776; the rest tracked in the follow-up issue |
| RI exchange | 7 (E1 to E7) | 1 (E5 `utilization_threshold` 0) | fixed in #1906 |
| Ladder | 1 (B6) | 0 | matches |
| Purchases/revoke | 7 (E8, E9, B7, T1 to T4), plus N1 (not an adapter) | 0 | matches |
| Overrides | 3 (O1, B8, B9) | 0 | O1 fixed in #1788 (#1775) |
| Auth/profile | 10 (U1 to U10) | 0 | matches; `internal/auth` conversions are #923 |
| Total | 42 | 7 | |

Pair ids are local to this document: A is accounts, R registrations, P and B plans, service config, ladder, marketplace and override import, E RI exchange and purchase execution, T approval-token bodies, O the override, U auth and profile.

## Accounts

### A1: CloudAccountRequest -> createAccount (POST /api/accounts)

- Area: accounts. Request type: CloudAccountRequest, internal/api/handler_accounts.go:32. Decode: handler_accounts.go:357 (validateCloudAccountRequest :359, requireHostForEmptyRoleARN :364).
- Target: config.CloudAccount, internal/config/types.go:1476. Converter: cloudAccountFromRequest, handler_accounts.go:548, called at :369. Store: PostgresStore.CreateCloudAccount, store_postgres.go:4049 (INSERT :4057-4073, args :4075-4100).
- Final disposition: matches.
- Covering guard for the converter: internal/api/adapter_field_drop_test.go:124 (converter output only; it does not prove the store write, which the rows below trace to the SQL args).

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| enabled | CloudAccount.Enabled (`*bool`, absent means false, :570-572) -> arg :4080 | reaches the target | adapter_field_drop_test.go:124 |
| aws_is_org_root | CloudAccount.AWSIsOrgRoot (:573-575) -> arg :4088 | reaches the target | adapter_field_drop_test.go:124 |
| aws_web_identity_token_file | CloudAccount.AWSWebIdentityTokenFile (:559) -> arg :4087 | reaches the target | adapter_field_drop_test.go:124 |
| azure_client_id | CloudAccount.AzureClientID (:562) -> arg :4091 | reaches the target | adapter_field_drop_test.go:124 |
| provider | CloudAccount.Provider (:553) -> arg :4081 | reaches the target | adapter_field_drop_test.go:124 |
| name | CloudAccount.Name (:550) -> arg :4077 | reaches the target | adapter_field_drop_test.go:124 |
| description | CloudAccount.Description (:551) -> arg :4078 | reaches the target | adapter_field_drop_test.go:124 |
| aws_auth_mode | CloudAccount.AWSAuthMode (:555) -> arg :4083 | reaches the target | adapter_field_drop_test.go:124 |
| aws_role_arn | CloudAccount.AWSRoleARN (:556) -> arg :4084 | reaches the target | adapter_field_drop_test.go:124 |
| aws_external_id | CloudAccount.AWSExternalID (:557) -> arg :4085 | reaches the target | adapter_field_drop_test.go:124 |
| contact_email | CloudAccount.ContactEmail (:552) -> arg :4079 | reaches the target | adapter_field_drop_test.go:124 |
| gcp_wif_audience | CloudAccount.GCPWIFAudience (:567) -> arg :4096 | reaches the target | adapter_field_drop_test.go:124 |
| external_id | CloudAccount.ExternalID (:554) -> arg :4082 | reaches the target | adapter_field_drop_test.go:124 |
| azure_subscription_id | CloudAccount.AzureSubscriptionID (:560) -> arg :4089 | reaches the target | adapter_field_drop_test.go:124 |
| azure_tenant_id | CloudAccount.AzureTenantID (:561) -> arg :4090 | reaches the target | adapter_field_drop_test.go:124 |
| aws_bastion_id | CloudAccount.AWSBastionID (:558) -> arg :4086 | reaches the target | adapter_field_drop_test.go:124 |
| azure_auth_mode | CloudAccount.AzureAuthMode (:563) -> arg :4092 | reaches the target | adapter_field_drop_test.go:124 |
| gcp_project_id | CloudAccount.GCPProjectID (:564) -> arg :4093 | reaches the target | adapter_field_drop_test.go:124 |
| gcp_client_email | CloudAccount.GCPClientEmail (:565) -> arg :4094 | reaches the target | adapter_field_drop_test.go:124 |
| gcp_auth_mode | CloudAccount.GCPAuthMode (:566) -> arg :4095 | reaches the target | adapter_field_drop_test.go:124 |

Notes: all targets are in handler_accounts.go (converter lines) and store_postgres.go (arg lines). Several fields are also validated (validateCloudAccountRequest :359, validateAuthMode, aws_external_id rules :498-523); precedence puts them under 'reaches the target'.

### A2: CloudAccountRequest -> updateAccount (PUT /api/accounts/:id)

- Area: accounts. Decode: handler_accounts.go:626. Converter: cloudAccountFromRequest :655, then keepOmittedAccountFlags :656 (defined :585). Store: PostgresStore.UpdateCloudAccount, store_postgres.go:4200 (UPDATE :4203-4224, args :4226-4247).
- Final disposition: matches. Only difference from A1: `provider` is checked, not written (UPDATE has no provider column); `enabled` and `aws_is_org_root` keep the stored value when omitted.
- Covering guard: adapter_field_drop_test.go:124 (converter), plus handler_accounts_update_keep_test.go:75 and :103 for the omitted-flag rule.

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| enabled | CloudAccount.Enabled -> arg :4231; omitted/null keeps stored via keepOmittedAccountFlags :586-588 | reaches the target | adapter_field_drop_test.go:124; handler_accounts_update_keep_test.go:75 |
| aws_is_org_root | CloudAccount.AWSIsOrgRoot -> arg :4238; omitted/null keeps stored :589-591 | reaches the target | adapter_field_drop_test.go:124; handler_accounts_update_keep_test.go:103 |
| aws_web_identity_token_file | CloudAccount.AWSWebIdentityTokenFile -> arg :4237 | reaches the target | adapter_field_drop_test.go:124 |
| azure_client_id | CloudAccount.AzureClientID -> arg :4241 | reaches the target | adapter_field_drop_test.go:124 |
| provider | none written: compared with the stored provider at handler_accounts.go:650-653; the comment at :646-649 says UpdateCloudAccount does not write it | validation or authorization only | none |
| name | CloudAccount.Name -> arg :4228 | reaches the target | adapter_field_drop_test.go:124 |
| description | CloudAccount.Description -> arg :4229 | reaches the target | adapter_field_drop_test.go:124 |
| aws_auth_mode | CloudAccount.AWSAuthMode -> arg :4233 | reaches the target | adapter_field_drop_test.go:124 |
| aws_role_arn | CloudAccount.AWSRoleARN -> arg :4234 | reaches the target | adapter_field_drop_test.go:124 |
| aws_external_id | CloudAccount.AWSExternalID -> arg :4235 | reaches the target | adapter_field_drop_test.go:124 |
| contact_email | CloudAccount.ContactEmail -> arg :4230 | reaches the target | adapter_field_drop_test.go:124 |
| gcp_wif_audience | CloudAccount.GCPWIFAudience -> arg :4246 | reaches the target | adapter_field_drop_test.go:124 |
| external_id | CloudAccount.ExternalID -> arg :4232 | reaches the target | adapter_field_drop_test.go:124 |
| azure_subscription_id | CloudAccount.AzureSubscriptionID -> arg :4239 | reaches the target | adapter_field_drop_test.go:124 |
| azure_tenant_id | CloudAccount.AzureTenantID -> arg :4240 | reaches the target | adapter_field_drop_test.go:124 |
| aws_bastion_id | CloudAccount.AWSBastionID -> arg :4236 | reaches the target | adapter_field_drop_test.go:124 |
| azure_auth_mode | CloudAccount.AzureAuthMode -> arg :4242 | reaches the target | adapter_field_drop_test.go:124 |
| gcp_project_id | CloudAccount.GCPProjectID -> arg :4243 | reaches the target | adapter_field_drop_test.go:124 |
| gcp_client_email | CloudAccount.GCPClientEmail -> arg :4244 | reaches the target | adapter_field_drop_test.go:124 |
| gcp_auth_mode | CloudAccount.GCPAuthMode -> arg :4245 | reaches the target | adapter_field_drop_test.go:124 |

### A3: CloudAccountRequest -> createSelfAccount (POST /api/accounts/self), no decode site

- Area: accounts. No request body is read: the request struct is built by buildSelfAccountRequest (handler_accounts.go:327, called :284) from the source identity, then converted by cloudAccountFromRequest (:290) and stored with CreateCloudAccount (:296). `account.Enabled = true` is forced at :294, which equals the builder's own `enabled: true` (:333), so nothing is lost. No API field rows (rows are keyed by API field; there is no body). Fields the builder sets: name, provider, external_id, enabled, aws_auth_mode (aws); azure_auth_mode, azure_subscription_id, azure_tenant_id, azure_client_id (azure); gcp_auth_mode, gcp_project_id (gcp). The remaining CloudAccountRequest fields are never set on this path (nothing to drop).
- Final disposition: matches (all values the builder sets pass through the same converter guarded at adapter_field_drop_test.go:124).

### A4: CredentialsRequest -> saveAccountCredentials (POST /api/accounts/:id/credentials)

- Area: accounts. Request type: CredentialsRequest, handler_accounts.go:56. Decode: handler_accounts.go:802 (parseAndValidateCredentialsRequest :793). Target: positional arguments 3 and 4 of `credStore.SaveCredential(ctx, id, credentialType, payload)` at :839; the callee is pgCredentialStore.SaveCredential, internal/credentials/store.go:50 (INSERT account_credentials :55-61). Converter: none (json.Marshal of the payload :834).
- Final disposition: matches.

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| credential_type | positional argument 3 (credType) of SaveCredential -> column credential_type (internal/credentials/store.go:56-61); allowlisted at :805 | reaches the target | none (handler_accounts_test.go:934 asserts success only) |
| payload | positional argument 4 of SaveCredential, marshalled as a whole value at :834 and encrypted (store.go:51) into encrypted_blob; per-type schema validated by validateCredentialPayload (internal/api/validation.go:236, called :808) | reaches the target (as a whole value) | none (handler_accounts_test.go:934 asserts success only) |

### A5: DiscoverOrgRequest -> discoverOrgAccounts (POST /api/accounts/discover-org)

- Area: accounts. Request type: DiscoverOrgRequest, handler_accounts.go:1599. Decode: handler_accounts.go:1661 (parseDiscoverOrgRoot :1659). Target: positional argument 3 of `requireAccountAccess(ctx, session, accountID)` (internal/api/scoping.go:32) which loads the root account from the store; the loaded `root.ID` (not the request value) is written as aws_bastion_id at :1791 through CreateCloudAccount.
- Final disposition: matches (lookup key only).

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| account_id | lookup key: validateUUID :1664, requireAccountAccess :1671 (scope check, then GetCloudAccount), then `root` drives AWS calls; the stored root.ID is what :1791 persists | validation or authorization only | internal/api/discover_org_scope_test.go:173 (unrestricted admin persists members; it does not assert the request value) |

## Registrations

### R1: RegistrationRequest -> submitRegistration (POST /api/register)

- Area: registrations. Request type: RegistrationRequest, handler_registrations.go:23. Decode: handler_registrations.go:91; auxiliary decode of the credential_payload string at handler_registrations_credentials.go:37 (parseRegistrationCredential :29, called from validateRegistrationRequest :495).
- Target: config.AccountRegistration, internal/config/types.go:1556. Converter: registrationFromRequest, handler_registrations.go:60 (called :104); credential fields attached in submitRegistration :106-114. Store: PostgresStore.CreateAccountRegistration, internal/config/store_postgres_registrations.go:60 (INSERT :68-88, args :90-100).
- Final disposition: matches (credential fields conditional on a credential store, deliberate).
- Guard: adapter_field_drop_test.go:128 (converter; `credential_type` and `credential_payload` are allowlisted there as "attached by submitRegistration", so the guard does not cover them).

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| provider | AccountRegistration.Provider (:64) -> store_postgres_registrations.go:92 | reaches the target | adapter_field_drop_test.go:128 |
| external_id | AccountRegistration.ExternalID (:65) -> :92 | reaches the target | adapter_field_drop_test.go:128 |
| account_name | AccountRegistration.AccountName (:66, normalized in place :466-469) -> :92 | reaches the target | adapter_field_drop_test.go:128 |
| contact_email | AccountRegistration.ContactEmail (:67, reduced to the bare address by registrationContactEmail :512) -> :92 | reaches the target | adapter_field_drop_test.go:128; handler_registrations_submit_test.go:139 |
| description | AccountRegistration.Description (:68) -> :92 | reaches the target | adapter_field_drop_test.go:128 |
| source_provider | AccountRegistration.SourceProvider (:69) -> :93 | reaches the target | adapter_field_drop_test.go:128 |
| aws_role_arn | AccountRegistration.AWSRoleARN (:70) -> :94 | reaches the target | adapter_field_drop_test.go:128 |
| aws_auth_mode | AccountRegistration.AWSAuthMode (:71) -> :94 | reaches the target | adapter_field_drop_test.go:128 |
| aws_external_id | AccountRegistration.AWSExternalID (:72) -> :94 | reaches the target | adapter_field_drop_test.go:128 |
| azure_subscription_id | AccountRegistration.AzureSubscriptionID (:73) -> :95 | reaches the target | adapter_field_drop_test.go:128 |
| azure_tenant_id | AccountRegistration.AzureTenantID (:74) -> :95 | reaches the target | adapter_field_drop_test.go:128 |
| azure_client_id | AccountRegistration.AzureClientID (:75) -> :96 | reaches the target | adapter_field_drop_test.go:128 |
| azure_auth_mode | AccountRegistration.AzureAuthMode (:76) -> :96 | reaches the target | adapter_field_drop_test.go:128 |
| gcp_project_id | AccountRegistration.GCPProjectID (:77) -> :97 | reaches the target | adapter_field_drop_test.go:128 |
| gcp_client_email | AccountRegistration.GCPClientEmail (:78) -> :97 | reaches the target | adapter_field_drop_test.go:128 |
| gcp_auth_mode | AccountRegistration.GCPAuthMode (:79) -> :98 | reaches the target | adapter_field_drop_test.go:128 |
| gcp_wif_audience | AccountRegistration.GCPWIFAudience (:80) -> :98 | reaches the target | adapter_field_drop_test.go:128 |
| credential_type | AccountRegistration.RegCredentialType (handler_registrations.go:112) -> store_postgres_registrations.go:99; set only when a payload was encrypted, i.e. a credential store exists; comment at :111 says "Without a credential store nothing is kept, so record no type either" | reaches the target (conditional, deliberate: :111) | handler_registrations_credentials_test.go:71; :129 (no credential store) |
| credential_payload | AccountRegistration.RegCredentialPayload (:113) via encryptRegistrationCredential (:258, h.credStore.EncryptPayload) -> :99; opaque JSON object string validated per credential type by parseRegistrationCredential (handler_registrations_credentials.go:29-48, validateCredentialPayload :40) and re-encoded canonically before encryption; stored as a whole value | reaches the target (as a whole value; dropped only when no credential store, deliberate :111) | handler_registrations_credentials_test.go:71; :129 |

### R2: CloudAccountRequest -> approveRegistration (POST /api/registrations/:id/approve)

- Area: registrations. Decode: handler_registrations.go:352 (parseApprovalRequest :350, called :286). Converter: cloudAccountFromRequest :294, then handler-set fields :295-305. Store: PostgresStore.ApproveAccountRegistration, store_postgres_registrations.go:277, which calls txStore.CreateCloudAccount (:285, same INSERT as A1).
- Final disposition: documented gap for `enabled` (fixed in #1905, after the audited commit); otherwise matches.
- Guard: adapter_field_drop_test.go:124 (converter output; the handler overwrites `Enabled` after the converter, so the guard does not see the drop).

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| enabled | CloudAccount.Enabled is assigned `regCred != nil \|\| accountHasCredentialFreePath(account)` at handler_registrations.go:303 (helper :371-391), replacing the converter's value; the comment at :296-302 explains the auto-enable rule but does not say the request value is ignored | **DROPPED** (fixed in #1905, after the audited commit) | handler_registrations_approve_test.go:61 asserts Enabled true from a body with no `enabled` key; none for an explicit false |
| aws_is_org_root | CloudAccount.AWSIsOrgRoot (handler_accounts.go:573-575) -> store_postgres.go:4088 | reaches the target | adapter_field_drop_test.go:124 |
| aws_web_identity_token_file | CloudAccount.AWSWebIdentityTokenFile -> :4087 | reaches the target | adapter_field_drop_test.go:124 |
| azure_client_id | CloudAccount.AzureClientID -> :4091 | reaches the target | adapter_field_drop_test.go:124 |
| provider | CloudAccount.Provider -> :4081; also selects the credential check, `registrationCredential(reg, acctReq.Provider)` :361 | reaches the target | adapter_field_drop_test.go:124; handler_registrations_approve_test.go:61 |
| name | CloudAccount.Name -> :4077 | reaches the target | adapter_field_drop_test.go:124 |
| description | CloudAccount.Description -> :4078 | reaches the target | adapter_field_drop_test.go:124 |
| aws_auth_mode | CloudAccount.AWSAuthMode -> :4083 | reaches the target | adapter_field_drop_test.go:124 |
| aws_role_arn | CloudAccount.AWSRoleARN -> :4084 | reaches the target | adapter_field_drop_test.go:124 |
| aws_external_id | CloudAccount.AWSExternalID -> :4085 | reaches the target | adapter_field_drop_test.go:124 |
| contact_email | CloudAccount.ContactEmail -> :4079 | reaches the target | adapter_field_drop_test.go:124 |
| gcp_wif_audience | CloudAccount.GCPWIFAudience -> :4096 | reaches the target | adapter_field_drop_test.go:124 |
| external_id | CloudAccount.ExternalID -> :4082 | reaches the target | adapter_field_drop_test.go:124; handler_registrations_approve_test.go:61 |
| azure_subscription_id | CloudAccount.AzureSubscriptionID -> :4089 | reaches the target | adapter_field_drop_test.go:124 |
| azure_tenant_id | CloudAccount.AzureTenantID -> :4090 | reaches the target | adapter_field_drop_test.go:124 |
| aws_bastion_id | CloudAccount.AWSBastionID -> :4086 | reaches the target | adapter_field_drop_test.go:124 |
| azure_auth_mode | CloudAccount.AzureAuthMode -> :4092 | reaches the target | adapter_field_drop_test.go:124 |
| gcp_project_id | CloudAccount.GCPProjectID -> :4093 | reaches the target | adapter_field_drop_test.go:124 |
| gcp_client_email | CloudAccount.GCPClientEmail -> :4094 | reaches the target | adapter_field_drop_test.go:124 |
| gcp_auth_mode | CloudAccount.GCPAuthMode -> :4095 | reaches the target | adapter_field_drop_test.go:124 |

Extra observation (not a field row): the approval body is not required to match the registration's own provider or external_id; the body wins for the created account. Not a drop.

### R3: RejectRequest -> rejectRegistration (POST /api/registrations/:id/reject)

- Area: registrations. Request type: RejectRequest, handler_registrations.go:53. Decode: handler_registrations.go:402 (only when the body is non-empty :401). Target: config.AccountRegistration.RejectionReason (:408). Store: PostgresStore.TransitionRegistrationStatus, store_postgres_registrations.go:211 (`rejection_reason = $3` :216, arg :236). Also forwarded to the registrant email (`RejectionReason: body.Reason` :421).
- Final disposition: matches.

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| reason | AccountRegistration.RejectionReason -> store_postgres_registrations.go:236 | reaches the target | none (handler_registrations_api_key_integration_test.go:149 sends a reason through the route but is an auth-route test) |

## Plans

### P1: anonymous `{account_ids}` body -> setPlanAccounts (PUT /api/plans/:id/accounts)

- Area: plans. Target type of the decode: anonymous `struct{ AccountIDs []string }`, handler_accounts.go:1458-1460. Decode: handler_accounts.go:1461. Target: positional argument 3 (`accountIDs`) of `config.SetPlanAccountsGated` (called at :1494 inside storePlanAccounts :1488, passed `body.AccountIDs` at :1480); store PostgresStore.SetPlanAccountsGated store_postgres.go:4689 -> setPlanAccountsTx (internal/config/store_postgres_plan_writes.go:66, INSERT plan_accounts :74-76).
- Final disposition: matches.

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| account_ids | positional argument 3 of SetPlanAccountsGated -> plan_accounts rows (store_postgres_plan_writes.go:73-76); also validated (validatePlanAccountIDs :1465/:1537), authorized (requirePlanAccountsAccess :1469) and provider-checked (:1476); FORWARDED wins | reaches the target | none (handler_accounts_test.go:1232 asserts success only, not the argument) |

Notes that apply to B1 and B2 (the PlanRequest guard and #1776):

- `requireFieldsReach` is internal/api/adapter_field_drop_test.go:96-122; `TestPlanRequestToPurchasePlan_NoFieldDropped` is :137-147 with the allowlist `description` ("accepted but dropped, tracked in #1776", :145) and `target_accounts` ("handled by createPlan/setPlanAccounts, not the converter", :146). The guard covers only the output of `toPurchasePlan` (types.go:890). It never sees the update merge (`mergeUpdatedPlan`, handler_plans.go:431), the second decode (handler_plans.go:323-327), `SetPlanAccounts`, or the store write.
- #1776 (open at the audited commit) lists: description dropped on create and update; target_accounts ignored on PUT /api/plans/{id}; approveRegistration enabled; service-config ramp_schedule on an existing row; frontend-only fields. All five items still hold at the base SHA for the rows here. #1776 does not list the service-config body `provider` and `service` rows in B5.
- The update path for target_accounts is unguarded: the allowlist entry only proves the converter does not map it, not that update ignores it deliberately. No source comment in handler_plans.go (mergeUpdatedPlan :431-469, updatePlan :303-346) says so.

### B1: PlanRequest -> createPlan (POST /api/plans)

- Request type: PlanRequest, internal/api/types.go:847. Decode: decodePlanRequest, internal/api/handler_plans.go:166-175 (site handler_plans.go:168).
- Target type: config.PurchasePlan, internal/config/types.go:256 (+ plan_accounts rows via SetPlanAccounts).
- Converter: `(*PlanRequest).toPurchasePlan`, types.go:890-907 (helpers buildRampSchedule :910, buildCustomRampSchedule :927, buildServiceConfig :953, calculateNextExecutionDate :988). Handler call: handler_plans.go:205.
- Store path: CreatePurchasePlan (handler_plans.go:212) -> createPurchasePlan, internal/config/store_postgres_plan_writes.go:13-64 (INSERT :32-57); SetPlanAccounts (handler_plans.go:235) -> setPlanAccountsTx, store_postgres_plan_writes.go:66-82.
- Per-pair disposition: documented gap #1776 (description dropped). All other fields reach the target.

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| name | PurchasePlan.Name (types.go:893) -> purchase_plans.name (store_postgres_plan_writes.go:35,46) | reaches the target | guard (converter only) adapter_field_drop_test.go:137; handler_plans_test.go:298 |
| description | none (toPurchasePlan never reads it; config.PurchasePlan has no description field, types.go:256-281) | **DROPPED** (tracked in #1776 item 1; no handler-source comment, only the test allowlist at adapter_field_drop_test.go:145) | adapter_field_drop_test.go:137 asserts it stays dropped |
| ramp_schedule | PurchasePlan.RampSchedule via buildRampSchedule (types.go:910-924) -> purchase_plans.ramp_schedule jsonb (store_postgres_plan_writes.go:27,50). Values outside the three presets and "custom" silently become "immediate" (types.go:919-923), see Findings; fixed in #1906, after the audited commit | reaches the target | guard (converter only) :137 (base uses "custom"); handler_plans_test.go:298 |
| provider | ServiceConfig.Provider via buildServiceConfig (types.go:953-985), key provider/service -> purchase_plans.services jsonb (marshalPlanServices, store_postgres_plan_writes.go:90-107). Reaches only when BOTH provider and service are non-empty (types.go:954-956); otherwise Services is nil | reaches the target | guard :137 (base sets provider and service); handler_plans_services_merge_test.go:183 |
| service | ServiceConfig.Service, same helper as provider | reaches the target | guard :137; handler_plans_services_merge_test.go:183 |
| target_accounts | positional argument 3 of h.config.SetPlanAccounts (handler_plans.go:235) -> plan_accounts INSERT (store_postgres_plan_writes.go:74-77); also validated (:195) and authorized (requireAccountsAccess :201) | reaches the target | handler_plans_test.go:298 (mock store asserts SetPlanAccounts args, :339); integration test handler_plans_legacy_version_integration_test.go:171 posts it |
| target_coverage | ServiceConfig.Coverage via buildServiceConfig (types.go:970-973), validated by validateTargetCoverage (types.go:870-875, called handler_plans.go:171) | reaches the target | guard :137; handler_plans_services_merge_test.go:183 |
| term | ServiceConfig.Term via buildServiceConfig (types.go:959-962) | reaches the target | guard :137; handler_plans_services_merge_test.go:183 |
| payment | ServiceConfig.Payment via buildServiceConfig (types.go:964-967) | reaches the target | guard :137; handler_plans_services_merge_test.go:183 |
| notification_days_before | PurchasePlan.NotificationDaysBefore (types.go:896) -> purchase_plans.notification_days_before | reaches the target | guard :137; handler_plans_test.go:298 does not set it |
| custom_step_percent | RampSchedule.PercentPerStep and TotalSteps via buildCustomRampSchedule (types.go:927-950); applies only when ramp_schedule is "custom" | reaches the target | guard :137 (custom base) |
| custom_interval_days | RampSchedule.StepIntervalDays via buildCustomRampSchedule (types.go:933-936); only when ramp_schedule is "custom" | reaches the target | guard :137 (custom base) |
| max_step_commitment | PurchasePlan.MaxStepCommitment (types.go:897) -> purchase_plans.max_step_commitment (store_postgres_plan_writes.go:37,56) | reaches the target | guard :137; handler_plans_step_cap_test.go:17 (merge path, not create) |
| auto_purchase | PurchasePlan.AutoPurchase (types.go:895); also drives the arming gate validateAndGatePlan (handler_plans.go:208) | reaches the target | guard :137; handler_plans_sp_multistep_test.go:43 |
| enabled | PurchasePlan.Enabled (types.go:894) | reaches the target | guard :137; handler_plans_test.go:298 (asserts Enabled) |

Create body note: `expected_updated_at` is not a field of the create body (only the update handler decodes it), so it has no row here.

### B2: PlanRequest (+ anonymous cap struct) -> updatePlan (PUT /api/plans/{id})

- Request types: PlanRequest (types.go:847) decoded at handler_plans.go:168 via decodePlanRequest (called :319), plus the anonymous `struct{MaxStepCommitment json.RawMessage; ExpectedUpdatedAt *string}` decoded at handler_plans.go:323-327 (auxiliary site :327). The field set is the union of both.
- Target type: config.PurchasePlan (types.go:256).
- Converter chain: loadPlanForUpdate (handler_plans.go:409-426) -> mergeUpdatedPlan (:431-469) -> `toPurchasePlan` (types.go:890) + preserveRampProgress (:485-512) + mergeRequestedServices / overlayServiceConfig (plan_services_merge.go:31-66, :100-114). Store: updatePlanGated (:355-383) -> UpdatePurchasePlan -> UpdatePurchasePlanGated (store_postgres.go:811) -> updatePurchasePlanGuardedTx (:878-923, UPDATE :893-907).
- Per-pair disposition: documented gap #1776 (description, target_accounts on update). Everything else reaches.

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| name | PurchasePlan.Name -> purchase_plans.name (store_postgres.go:897) | reaches the target | guard (converter only) adapter_field_drop_test.go:137; handler_plans_test.go:571; handler_plans_managed_rename_integration_test.go:13 |
| description | none | **DROPPED** (#1776 item 1; no handler-source comment) | adapter_field_drop_test.go:137 asserts it stays dropped |
| ramp_schedule | PurchasePlan.RampSchedule via buildRampSchedule then preserveRampProgress (handler_plans.go:485-512): same shape keeps the stored CurrentStep, StartDate, NextExecutionDate; a different shape is applied before the first step and refused with 409 once a step completed. Unknown values become "immediate" (types.go:919-923) | reaches the target | handler_plans_ramp_shape_test.go:51; handler_plans_test.go:619 |
| provider | ServiceConfig.Provider, merged into the stored services map by mergeRequestedServices (plan_services_merge.go:41-66). If only one of provider/service is sent, buildServiceConfig returns nil (types.go:954-956) and the sent one is silently ignored | reaches the target | handler_plans_services_merge_test.go:36,172 |
| service | ServiceConfig.Service, same path | reaches the target | handler_plans_services_merge_test.go:36,172 |
| target_accounts | none. mergeUpdatedPlan never reads req.TargetAccounts; the frontend uses PUT /api/plans/{id}/accounts instead (#1776 item 2). The arming and account-scope gates for accounts run only on that other endpoint | **DROPPED** (#1776 item 2; no source comment on the update path; the test allowlist comment at adapter_field_drop_test.go:146 only says the converter does not map it) | none on the update path (the guard allowlists it; handler_plans_test.go:571 sends no target_accounts) |
| target_coverage | ServiceConfig.Coverage; omitted keeps the stored value (omittedFields, types.go:881-887; overlayServiceConfig plan_services_merge.go:110-112) | reaches the target | handler_plans_services_merge_test.go:139,172 |
| term | ServiceConfig.Term; nil or 0 keeps the stored value (types.go:884; plan_services_merge.go:104-106) | reaches the target | handler_plans_services_merge_test.go:147,172,206 |
| payment | ServiceConfig.Payment; nil or "" keeps the stored value (types.go:885; plan_services_merge.go:107-109) | reaches the target | handler_plans_services_merge_test.go:155,172,206 |
| notification_days_before | PurchasePlan.NotificationDaysBefore (types.go:896) -> UPDATE notification_days_before (store_postgres.go:898). Non-pointer: an omitted key is 0 and overwrites the stored value (observation, not a drop) | reaches the target | guard :137; handler_plans_test.go:571 |
| custom_step_percent | RampSchedule.PercentPerStep / TotalSteps (types.go:927-950); only when ramp_schedule is "custom" | reaches the target | guard :137; handler_plans_ramp_shape_test.go:101 |
| custom_interval_days | RampSchedule.StepIntervalDays (types.go:933-936); only when ramp_schedule is "custom" | reaches the target | guard :137; handler_plans_ramp_shape_test.go:101 |
| max_step_commitment | PurchasePlan.MaxStepCommitment: decoded twice. PlanRequest.MaxStepCommitment gives the value; the anonymous struct (handler_plans.go:324,336) gives presence, so omitted keeps the stored cap (:449-451) and explicit null clears it unless auto_purchase is on (:452-454). Validated by plan.Validate (config/validation.go:614-617) -> UPDATE max_step_commitment (store_postgres.go:904,922) | reaches the target | handler_plans_step_cap_test.go:17; handler_plans_concurrency_integration_test.go:330 (Postgres) |
| auto_purchase | PurchasePlan.AutoPurchase; arming gate planArmEscalates/requirePlanArmGrantFor (handler_plans.go:361-371). Non-pointer: omitted means false | reaches the target | guard :137; handler_plans_sp_multistep_test.go:50 |
| enabled | PurchasePlan.Enabled; non-pointer: omitted means false (observation) | reaches the target | guard :137; handler_plans_test.go:571 |
| expected_updated_at | none stored. Used only to compare against the stored updated_at (loadPlanForUpdate handler_plans.go:416-424, 409 on mismatch, 400 on bad format); the write then uses the stored updated_at as the optimistic key (mergeUpdatedPlan :437-438, SQL `WHERE ... updated_at = $8` store_postgres.go:905) | validation or authorization only (concurrency token) | handler_plans_legacy_version_integration_test.go:100-135 (Postgres) |

### B3: PatchPlanRequest -> patchPlan (PATCH /api/plans/{id})

- Request type: PatchPlanRequest, handler_plans.go:902-907. Decode: handler_plans.go:969 (site).
- Target type: config.PurchasePlan. Converter: applyPatchFields, handler_plans.go:927-950 (L1) -> updatePlanGated (L2) -> UpdatePurchasePlan -> updatePurchasePlanGuardedTx UPDATE (L3, store_postgres.go:893-907).
- Per-pair disposition: matches.

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| name | PurchasePlan.Name (handler_plans.go:935) -> UPDATE name | reaches the target | handler_plans_test.go:1198 |
| enabled | PurchasePlan.Enabled (:938) -> UPDATE enabled | reaches the target | handler_plans_test.go:1198 |
| auto_purchase | PurchasePlan.AutoPurchase (:941) -> UPDATE auto_purchase; arming gate in updatePlanGated | reaches the target | handler_plans_test.go:1198; handler_plans_sp_multistep_test.go:76 |
| notification_days_before | PurchasePlan.NotificationDaysBefore (:947) -> UPDATE notification_days_before | reaches the target | handler_plans_test.go:1198 |

### B4: CreatePlannedPurchasesRequest -> createPlannedPurchases (POST /api/plans/{id}/purchases)

- Request type: CreatePlannedPurchasesRequest, types.go:1003-1006. Decode: parseCreatePurchasesRequest, handler_plans.go:808-824 (site :810).
- Target: positional arguments of createPlannedPurchasesTx (handler_plans.go:606) -> purchase_executions rows and the plan row. Chain: createPlannedPurchasesTx (L1, :633) -> createPurchaseExecutionsTx (L2, :860-887) -> SavePurchaseExecutionTx (L3, store_postgres.go:1143, INSERT :1176-1186, args :1236-1262); updatePlanNextExecutionDateTx (:892-899) -> UpdatePurchasePlanTx.
- Per-pair disposition: matches.

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| start_date | positional argument 5 of createPlannedPurchasesTx (parsed to time.Time at :818); becomes PurchaseExecution.ScheduledDate (:865-876) -> purchase_executions.scheduled_date (store_postgres.go:1241), and PurchasePlan.NextExecutionDate / RampSchedule.StartDate (:893-894) -> UPDATE next_execution_date, ramp_schedule | reaches the target | handler_plans_concurrency_integration_test.go:135 (Postgres); handler_plans_start_date_test.go:92 |
| count | positional argument 4 of createPlannedPurchasesTx (req.Count, :606); number of rows and StepNumber (:863-875) -> purchase_executions.step_number; also validated 1..52 (:814) and against remaining ramp steps (:740) | reaches the target | handler_plans_concurrency_integration_test.go:135,188 |

### B5: config.ServiceConfig body -> updateServiceConfig overlay (PUT /api/config/service/{provider}/{service})

- Request type: config.ServiceConfig (internal/config/types.go:214-253), decoded directly into the store struct at handler_config.go:499 (site). Presence map: presentKeys, handler_config.go:387-399 (site :389, auxiliary).
- Why this is a pair although the decode is into a store struct: for an EXISTING row, mergeServiceConfig (handler_config.go:369-382) copies an allowlist (serviceConfigOverlayKeys, :351-357) from the decoded struct onto the stored row, so a decoded field outside the allowlist never reaches the store. For a first save (no stored row) the decoded struct is saved as is (:370-372).
- Target type: config.ServiceConfig row `merged` (handler_config.go:378), saved by UpdateServiceConfigAtomic -> saveServiceConfigWith (store_postgres.go:471-560, INSERT/UPSERT :526-546).
- Per-pair disposition: documented gap #1776 item 4 (ramp_schedule on an existing row; comment exists at :349-350); not in #1776: body provider and service are overwritten by the URL path (DROPPED, below; see Findings).

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| provider | none: cfg.Provider is overwritten from the URL path at handler_config.go:510-514 (path always has exactly two parts, validation.go:441-445) | **DROPPED** (path wins; no source comment; body value silently ignored; low severity, not on a money path) | handler_config_test.go:624 sends no provider in the body; none asserts a conflicting body value |
| service | none: cfg.Service overwritten from the URL path, same lines | **DROPPED** (same) | none |
| enabled | ServiceConfig.Enabled, overlaid only when the key is present (overlayPresentScalarFields :405-408; first save: whole cfg) -> service_configs.enabled (store_postgres.go:532) | reaches the target | handler_config_test.go:727,776 |
| term | ServiceConfig.Term, presence overlay (:409-411) -> term | reaches the target | handler_config_test.go:727 |
| payment | ServiceConfig.Payment, presence overlay (:412-414) -> payment | reaches the target | handler_config_test.go:727 |
| coverage | ServiceConfig.Coverage, presence overlay (:415-417) -> coverage | reaches the target | handler_config_test.go:727 |
| ramp_schedule | none for an existing row (not in serviceConfigOverlayKeys); honoured on first save (mergeServiceConfig :370-372) | deliberately ignored (existing row only), source comment handler_config.go:349-350 ("RampSchedule is intentionally absent: it is still set out-of-band and the UI does not own it"); tracked #1776 item 4 (the API spec and a frontend comment are the stale parts) | none specific (handler_config_test.go:661 builds no ramp_schedule) |
| include_engines | ServiceConfig.IncludeEngines, presence overlay (overlayPresentFilterFields :425-427) -> include_engines | reaches the target | handler_config_test.go:661 |
| exclude_engines | ExcludeEngines (:428-430) | reaches the target | handler_config_test.go:661 |
| include_regions | IncludeRegions (:431-433) | reaches the target | handler_config_test.go:661 |
| exclude_regions | ExcludeRegions (:434-436) | reaches the target | handler_config_test.go:661 |
| include_types | IncludeTypes (:437-439) | reaches the target | handler_config_test.go:661 |
| exclude_types | ExcludeTypes (:440-442) | reaches the target | handler_config_test.go:661 (asserts exclude_types preserved when absent) |
| min_count | ServiceConfig.MinCount (:443-445) -> min_count | reaches the target | handler_config_test.go:661 |
| created_at | none: cleared at handler_config.go:505 | deliberately ignored, source comment handler_config.go:502-504 (read-only row stamps, issue #8) | none |
| updated_at | none: cleared at :505 | deliberately ignored, comment :502-504 | none |
| group_id | none: cleared at :508 | deliberately ignored, source comment :506-507 (plan-group fields belong to plan entries) | handler_config_test.go:1698 (first save drops bound_count) |
| bound_identities | none: cleared at :508 | deliberately ignored, comment :506-507 | none |
| max_step_commitment | none: cleared at :508 (the service_configs upsert has no such column, store_postgres.go:526-530) | deliberately ignored, comment :506-507 | none |
| bound_count | none: cleared at :508 | deliberately ignored, comment :506-507 | handler_config_test.go:1698 |

## RI exchange

### E1: ExchangeQuoteRequestBody via getExchangeQuote

- Request type: ExchangeQuoteRequestBody, internal/api/handler_ri_exchange.go:2224-2230; element type ExchangeTargetBody :2214-2217.
- Decode site: handler_ri_exchange.go:1956 (appendix row 3).
- Target type: exchange.ExchangeQuoteRequest (pinned pkg exchange/exchange.go:72-97).
- Converting function: getExchangeQuote :1972-1978, with toExchangeTargets :2251-2260 for `targets`.
- Path: handler (0) builds ExchangeQuoteRequest and calls the quoter chosen at ri_exchange_detached.go:45 (exchange.GetExchangeQuote) (2); getQuoteWithAPI/quoteWithAPI (pinned pkg exchange.go:277-300) build the EC2 GetReservedInstancesExchangeQuoteInput (3). No store write: this is a read-only quote.
- Final disposition: matches.

| API json tag | Target | Disposition | Covering test |
| --- | --- | --- | --- |
| ri_ids | ExchangeQuoteRequest.ReservedIDs (:1974), then EC2 input ReservedInstanceIds (pinned pkg exchange.go:298) | reaches the target | none for the library request; validateExchangeRIIDs (:1908) tested via handler_ri_exchange_test.go:108 |
| targets[].offering_id | TargetConfig.OfferingID via toExchangeTargets (:2257), then TargetConfigurationRequest.OfferingId (pinned pkg exchange.go:142) | reaches the target | handler_ri_exchange_test.go:1892 (asserts quotes[0].Targets[0].OfferingID) |
| targets[].count | TargetConfig.Count via toExchangeTargets (:2257), then InstanceCount (pinned pkg exchange.go:143) | reaches the target | none |
| target_offering_id (legacy) | ExchangeQuoteRequest.TargetOfferingID (:1976); NOT folded into targets by the handler; the library uses it only when targets is empty (pinned pkg exchange.go:84-86 and :137-152) | reaches the target when `targets` is empty; deliberately ignored when `targets` is non-empty, cited at handler_ri_exchange.go:2221-2223 ("When both are present, targets wins") | handler_ri_exchange_test.go:2297 (legacy body reaches the quote call; asserts only Region) |
| target_count (legacy) | ExchangeQuoteRequest.TargetCount (:1977); same precedence as target_offering_id | reaches the target when `targets` is empty; deliberately ignored when `targets` is non-empty, same citation | none |
| region | loadAWSConfigWithRegion (:1385-1394) overrides cfg.Region when non-empty; ExchangeQuoteRequest.Region = cfg.Region (:1970, :1973); pinned pkg loadCfg builds the SDK client for that region (exchange.go:235-240) | reaches the target (an empty value resolves from the SDK chain, documented at :1963-1965) | handler_ri_exchange_test.go:2275-2300 |

### E2: ExchangeExecuteRequestBody via executeExchange

- Request type: ExchangeExecuteRequestBody, handler_ri_exchange.go:2238-2245; ExchangeTargetBody :2214-2217.
- Decode site: handler_ri_exchange.go:2029 (appendix row 4).
- Target types, three destinations built from the one body: (A) exchange.ExchangeExecuteRequest (pinned pkg exchange.go:102-124), built at :2071-2078; (B) config.RIExchangeDirectExecution (internal/config/ri_exchange_direct_execution.go:39-66) built by awsDirectExchangeAudit (ri_exchange_audit.go:184-201); (C) the submit idempotency key built by awsExchangeIdempotencyKey (ri_exchange_idempotency.go:137-161).
- Many-to-one fold: the legacy `target_offering_id` and `target_count` become one element of targets in (B) (ri_exchange_audit.go:185-188, named helper: the inline fallback in awsDirectExchangeAudit) and in (C) (ri_exchange_idempotency.go:148-156). In (A) there is no fold in the handler: both spellings are forwarded and the pinned pkg chooses (buildTargetConfigs, exchange.go:137-152).
- Path for (A): handler (0), toExchangeTargets (1), exchange.ExecuteExchange via ri_exchange_detached.go:36 (2), executeWithAPI and the EC2 Accept input (3, pinned pkg exchange.go:419-462). Path for (B): awsDirectExchangeAudit (1), beginDirectExchange and SaveRIExchangeDirectExecution (2), the INSERT in internal/config/store_postgres_ri_exchange_direct.go:33-66 (3).
- Final disposition: matches.

| API json tag | Target | Disposition | Covering test |
| --- | --- | --- | --- |
| ri_ids | (A) ReservedIDs :2073, then Accept input ReservedInstanceIds (pinned pkg exchange.go:459); (B) SourceIDs (ri_exchange_audit.go:194), column source_ids (store_postgres_ri_exchange_direct.go:56); (C) sources component | reaches the target | ri_exchange_audit_test.go:185 (audit row SourceIDs) |
| targets[].offering_id | (A) TargetConfig via toExchangeTargets, then Accept input OfferingId (pinned pkg exchange.go:142); (B) Targets JSON via auditJSON (ri_exchange_audit.go:195), column targets (store_postgres_ri_exchange_direct.go:58); (C) target tuple | reaches the target | ri_exchange_audit_test.go:186 (audit row targets JSON) |
| targets[].count | same three destinations as offering_id | reaches the target | ri_exchange_audit_test.go:186 |
| target_offering_id (legacy) | (A) TargetOfferingID :2075, used by the pinned pkg only when targets is empty; (B) folded into a one-element Targets JSON (ri_exchange_audit.go:187); (C) folded into the target tuple (ri_exchange_idempotency.go:155) | reaches the target when `targets` is empty; deliberately ignored in (A) when `targets` is non-empty, cited at handler_ri_exchange.go:2221-2223 (and the same precedence comment at ri_exchange_idempotency.go:132-136) | ri_exchange_audit_test.go:547-561 (legacy singleton recorded as one-element targets) |
| target_count (legacy) | same as target_offering_id | same as target_offering_id | ri_exchange_audit_test.go:547-561 |
| max_payment_due_usd | parsed to maxRat (:2038); (A) MaxPaymentDueUSD :2077, enforced against the quote in the pinned pkg (exchange.go:387, :405); (B) MaxPaymentDue via auditDecimal (ri_exchange_audit.go:196), column max_payment_due (store_postgres_ri_exchange_direct.go:59); also the Constraints cap input at :2755 | reaches the target (it is also the spend-cap guardrail; FORWARDED wins) | ri_exchange_audit_test.go:187 (audit row cap); handler_ri_exchange_test.go:1991 (constraint use) |
| region | (A) Region :2072, pinned pkg loadCfg (exchange.go:235-240); (B) Region (ri_exchange_audit.go:193), column region (store_postgres_ri_exchange_direct.go:55); permission Constraints Regions at :2754; (C) account scope component. Required non-empty by validateExecuteExchangeBody :2006 | reaches the target | ri_exchange_audit_test.go:184 |

### E3: AzureCompatibleOfferingsRequestBody via getAzureCompatibleOfferings

- Request type: AzureCompatibleOfferingsRequestBody, handler_ri_exchange.go:524-528; elements AzureExchangeSourceBody :497-500 and AzureExchangeTargetBody :506-520.
- Decode site: handler_ri_exchange.go:981 (appendix row 1).
- Target types: azurecompute.ExchangeableReservation (alias azureExchangeableReservation, provider_wiring.go:40) and azurecompute.ExchangeTarget (alias :41), consumed by client.CalculateExchange (handler_ri_exchange.go:1009).
- Converting functions: toAzureExchangeSources :677-688 (applies quantities onto the server-listed reservation records after requireAzureSourceOwnership :916), toAzureExchangeTargets :696-713.
- Path: handler (0), converters (1), azurecompute client CalculateExchange (2), buildCalculateExchangeRequest building the ARM CalculateExchangeRequest (3; pinned azure provider services/compute/exchange_operations.go:498-544). No store write: read-only quote.
- Final disposition: matches (billing_scope_id deliberately not forwarded).

| API json tag | Target | Disposition | Covering test |
| --- | --- | --- | --- |
| subscription_id | account lookup and scope check (:987, lookupAzureAccount :879); client credentials (:991); derived billing scope via azureBillingScopeID (:596) into each target's BillingScopeID (:709); ownership key (:916-931) | reaches the target (as the derived billing scope of every target) | handler_ri_exchange_azure_test.go:423 (TestToAzureExchangeTargets_DerivesBillingScope), :531 |
| sources[].reservation_id | key into the server's tenant listing (toAzureExchangeSources :684); ownership check requireAzureSourceOwnership (:916); the listed record's ReservationID reaches ReservationsToExchange (pinned azure provider exchange_operations.go:503-506) | reaches the target (as a lookup key; the written id is the listed one) | handler_ri_exchange_azure_test.go:1801 |
| sources[].quantity | ExchangeableReservation.Quantity (:685), then ReservationToReturn.Quantity (pinned azure provider exchange_operations.go:504) | reaches the target | none (:381 registers CalculateExchange with mock.Anything for sources and targets) |
| targets[].sku | azureExchangeTarget.SKU (:705), then SKUName.Name (pinned azure provider exchange_operations.go:521) | reaches the target | none for the Azure call (:381 uses mock.Anything for sources and targets; :423 asserts only BillingScopeID) |
| targets[].location | Target.Location (:706), then PurchaseRequest.Location (pinned azure provider :520) | reaches the target | none for the Azure call (:381 uses mock.Anything for sources and targets; :423 asserts only BillingScopeID) |
| targets[].term | azureReservationTermFromString (provider_wiring.go:91-99), Target.Term (:707), then PurchaseRequestProperties.Term (pinned azure provider :530) | reaches the target | none for the Azure call (:381 uses mock.Anything for sources and targets; :423 asserts only BillingScopeID) |
| targets[].quantity | Target.Quantity (:708), then PurchaseRequestProperties.Quantity (pinned azure provider :527) | reaches the target | none for the Azure call (:381 uses mock.Anything for sources and targets; :423 asserts only BillingScopeID) |
| targets[].billing_scope_id | none (validated at :626-630, then the target's BillingScopeID is set from the derived scope at :709) | validation or authorization only; the deliberate non-use is documented at handler_ri_exchange.go:512-518 and :583-595 | handler_ri_exchange_azure_test.go:255, :440 |

### E4: AzureExecuteExchangeRequestBody via executeAzureExchange

- Request type: AzureExecuteExchangeRequestBody, handler_ri_exchange.go:544-550; elements as in E3.
- Decode site: handler_ri_exchange.go:1222, inside parseAzureExecuteRequest :1220-1233 (appendix row 2).
- Target types: (A) azureExchangeableReservation and azureExchangeTarget for the Azure client's re-quote (CalculateExchange :1279); (B) config.RIExchangeDirectExecution built by azureDirectExchangeAudit (ri_exchange_audit.go:239-259); (C) the idempotency key (ri_exchange_idempotency.go:103-121); (D) the permission Constraints set (checkAzureExecuteConstraints :1140).
- Converting functions: toAzureExchangeSources, toAzureExchangeTargets (as E3); azureDirectExchangeAudit.
- Path for (B): azureDirectExchangeAudit (1), beginDirectExchange and SaveRIExchangeDirectExecution (2), INSERT in store_postgres_ri_exchange_direct.go:33-66 (3). The commit call is ExecuteExchange with the session the fresh re-quote returned (:1310), so the request's sources and targets reach Azure through the re-quote only.
- Final disposition: matches.

| API json tag | Target | Disposition | Covering test |
| --- | --- | --- | --- |
| subscription_id | (A) client credentials and derived scope as E3; (B) ProviderAccountID (ri_exchange_audit.go:248), column provider_account_id (store_postgres_ri_exchange_direct.go:54); (C) key component (ri_exchange_idempotency.go:119); account lookup :1062 | reaches the target | ri_exchange_audit_test.go:424 |
| sources[].reservation_id | (A) as E3; (B) SourceIDs (ri_exchange_audit.go:241-243) and Sources JSON (:250); (C) key | reaches the target | ri_exchange_audit_test.go:426-427 |
| sources[].quantity | (A) as E3; (B) Sources JSON (:250); (C) key | reaches the target | ri_exchange_audit_test.go:427 |
| targets[].sku | (A) as E3; (B) Targets JSON (:251); (C) key | reaches the target | ri_exchange_audit_test.go:428 |
| targets[].location | (A) as E3; (B) Targets JSON; (C) key; (D) Regions constraint via targetLocations (:735) | reaches the target | ri_exchange_audit_test.go:428; handler_ri_exchange_azure_test.go:1411 |
| targets[].term | (A) as E3; (B) Targets JSON; (C) key | reaches the target | ri_exchange_audit_test.go:428 |
| targets[].quantity | (A) as E3; (B) Targets JSON; (C) key | reaches the target | ri_exchange_audit_test.go:428 |
| targets[].billing_scope_id | (A) none (the target's scope is the derived one, :709); (B) recorded as submitted inside the Targets JSON (ri_exchange_audit.go:251 marshals body.Targets whole; omitted when empty); (C) excluded on purpose (ri_exchange_idempotency.go:89-92) | reaches the target, audit JSON only (FORWARDED wins over validation); the Azure charge never uses it, per the comment at handler_ri_exchange.go:512-518 | handler_ri_exchange_azure_test.go:470 (foreign scope rejected); the audit JSON for a spelled-out scope has no test |
| max_payment_due | parsed to maxRat (:1228); (B) MaxPaymentDue via auditDecimal (ri_exchange_audit.go:252), column max_payment_due; compared with the fresh quote's net payable (checkAzureExchangeMoneyGuardrails :1206); Constraints MaxPurchaseAmount (:1151-1152) | reaches the target (spend cap; FORWARDED wins) | ri_exchange_audit_test.go:429; handler_ri_exchange_azure_test.go:1125 |
| currency | (B) MaxPaymentCurrency (ri_exchange_audit.go:253), column max_payment_currency; compared with the quote currency (:1199); picks the USD or non-USD cap path (:1148) | reaches the target | ri_exchange_audit_test.go:430; handler_ri_exchange_azure_test.go:1174 |

### E5: RIExchangeConfigUpdateRequest via updateRIExchangeConfig

- Request type: RIExchangeConfigUpdateRequest, handler_ri_exchange.go:3103-3110; validate :3112-3129.
- Decode site: handler_ri_exchange.go:2305 (appendix row 5).
- Target type: config.GlobalConfig (internal/config/types.go, fields RIExchange*, at :40 for the threshold).
- Converting function: the closure passed to UpdateGlobalConfigAtomic, handler_ri_exchange.go:2327-2343.
- Path: handler closure (1), UpdateGlobalConfigAtomic (store_postgres.go:198-225) (2), saveGlobalConfigWith UPSERT (store_postgres.go:263-316, args :358-390) (3).
- Final disposition: documented gap: one row loses a value (`utilization_threshold` 0), fixed in #1906 after the audited commit.

| API json tag | Target | Disposition | Covering test |
| --- | --- | --- | --- |
| mode | GlobalConfig.RIExchangeMode (:2336), column ri_exchange_mode (store_postgres.go:292, arg :367); an empty value would be stored as "manual" (store_postgres.go:319-322) but validate rejects it first (:3113) | reaches the target | none for this path (handler_config_ri_exchange_mode_test.go:57-83 covers the PUT /api/config path, a different decode site) |
| utilization_threshold | GlobalConfig.RIExchangeUtilizationThreshold (:2337), column ri_exchange_utilization_threshold (store_postgres.go:293, arg :368) | reaches the target for 0 < value <= 100; the value 0 is **DROPPED** (fixed in #1906, after the audited commit): accepted by validate (:3116) and stored as 95.0 (store_postgres.go:333-336), no comment saying 0 means "unset" | none (utilization_threshold appears only in request bodies in internal/api tests; the one stored-value assertion, handler_config_test.go:252, is the PUT /api/config path) |
| max_payment_per_exchange_usd | GlobalConfig.RIExchangeMaxPerExchangeUSD (:2338), column ri_exchange_max_per_exchange_usd (store_postgres.go:294, arg :369) | reaches the target | none |
| max_payment_daily_usd | GlobalConfig.RIExchangeMaxDailyUSD (:2339), column ri_exchange_max_daily_usd (store_postgres.go:295, arg :370) | reaches the target | none |
| lookback_days | GlobalConfig.RIExchangeLookbackDays (:2340), column ri_exchange_lookback_days (store_postgres.go:296, arg :371); a 0 would be stored as 30 (store_postgres.go:323-326) but validate rejects values below 1 (:3119) | reaches the target | none |
| auto_exchange_enabled | GlobalConfig.RIExchangeEnabled (:2335), column ri_exchange_enabled (store_postgres.go:291, arg :366); also drives the auto-mode arm gate (requireRIExchangeAutoModeGrant :2342) | reaches the target | global_config_arming_grants_test.go:70 (arming body, exercises the gate; asserts no stored column) |

### E6: riExchangeResolveRequest via resolveRIExchange

- Request type: riExchangeResolveRequest, handler_ri_exchange_resolve.go:46-52; riExchangeResolveCloudCheck :54-58.
- Decode site: handler_ri_exchange_resolve.go:208 (appendix row 6); the EOF check at :211 is auxiliary (row 7).
- Target type: config.RIExchangeResolve (internal/config/store_postgres_ri_exchange_resolve.go:46-54) with config.RIExchangeCloudCheck (:34-38).
- Converting functions: body.note (:271-281), body.cloudCheck (:256-267), the struct literal at :96-99.
- Path: converters (1), store.ResolveStaleRIExchange (store_postgres_ri_exchange_resolve.go:86-108) (2), applyRIExchangeResolve UPDATE ri_exchange_history (:207-227, SQL :208-219) (3). Note: the stored text is the note, which folds reason, acknowledgement, account, region and time into the `error` column.
- Final disposition: matches.

| API json tag | Target | Disposition | Covering test |
| --- | --- | --- | --- |
| outcome | RIExchangeResolve.Outcome (:97), UPDATE status = $2 (store :210, arg :219) | reaches the target | handler_ri_exchange_resolve_test.go:75, :89 |
| reason | folded into RIExchangeResolve.Note by body.note (:279), UPDATE error = $3 (store :211) | reaches the target | handler_ri_exchange_resolve_test.go:81 |
| exchange_id | RIExchangeResolve.ExchangeID (:97), UPDATE exchange_id (store :212); also in the note (:276-278) | reaches the target | handler_ri_exchange_resolve_test.go:90-91 |
| acknowledge_unknown_outcome | required when outcome is "failed" (:247-249); stored as `ack=true` in the note when outcome is "failed" (:273-275), written to the `error` column | reaches the target when outcome is "failed"; for outcome "completed" a true value is accepted and not stored: deliberately ignored, cited at handler_ri_exchange_resolve.go:43-45 (the comment makes the acknowledgement a requirement of "failed" only). Judgment call: if that comment is read as not documenting an ignore, this becomes an ordinary DROPPED cell (a "completed" resolve releases nothing) | handler_ri_exchange_resolve_test.go:81 (failed note has ack=true), :92 (completed note has none) |
| cloud_check.checked_account_id | RIExchangeCloudCheck.AccountID (:265); compared with the row's account in the store (store_postgres_ri_exchange_resolve.go:181); also in the note (:272) | reaches the target | handler_ri_exchange_resolve_test.go:78, :81 |
| cloud_check.checked_region | RIExchangeCloudCheck.Region (:265); compared with the row's region (store :183); also the Regions constraint input (:89, :116-153) and the note | reaches the target | handler_ri_exchange_resolve_test.go:78, :81 |
| cloud_check.checked_at | RIExchangeCloudCheck.CheckedAt (:265, parsed at :258); compared with the DB clock and the row's last change (store :185-189); also in the note | reaches the target | handler_ri_exchange_resolve_test.go:78, :81 |

### E7: riExchangeDirectResolveRequest via resolveRIExchangeDirectExecution

- Request type: riExchangeDirectResolveRequest, handler_ri_exchange_direct_resolve_request.go:18-24; riExchangeDirectResolveCloudCheck :26-29.
- Decode site: handler_ri_exchange_direct_resolve_request.go:41 (appendix row 8); EOF check :44 auxiliary (row 9).
- Target type: config.RIExchangeDirectResolve (internal/config/ri_exchange_direct_resolve.go:29-37).
- Converting functions: buildRIExchangeDirectResolve (handler_ri_exchange_direct_resolve.go:195-208), body.note (direct_resolve_request.go:98-105), body.checkedAt (:81-91).
- Path: converters (1), store.ResolveRIExchangeDirectExecution (store_postgres_ri_exchange_direct_resolve.go:28-53) (2), applyRIExchangeDirectResolve UPDATE ri_exchange_direct_executions (:140-156, SQL :141-147) (3).
- Final disposition: matches.

| API json tag | Target | Disposition | Covering test |
| --- | --- | --- | --- |
| outcome | RIExchangeDirectResolve.Outcome (handler_ri_exchange_direct_resolve.go:205), UPDATE resolved_status = $2 | reaches the target | handler_ri_exchange_direct_resolve_test.go:114, :132 |
| expected_status | RIExchangeDirectResolve.ExpectedStatus (:205), UPDATE ... AND status = $6 (store :145, arg :147); also in the note (direct_resolve_request.go:100) | reaches the target | handler_ri_exchange_direct_resolve_test.go:115, :209 |
| reason | folded into Note by body.note (direct_resolve_request.go:104), UPDATE resolution_note = $5 | reaches the target | handler_ri_exchange_direct_resolve_test.go:120; handler_ri_exchange_direct_resolve_body_test.go:70 |
| acknowledge_unknown_outcome | required when outcome is "failed" (direct_resolve_request.go:73-74); stored as `ack=true` in the note when outcome is "failed" (:101-103) | reaches the target when outcome is "failed"; for "completed" a true value is accepted and not stored: deliberately ignored, cited at handler_ri_exchange_direct_resolve_request.go:14-17 (requirement stated for "failed" only). Same judgment call as E6 | handler_ri_exchange_direct_resolve_test.go:120 (failed), :132 (completed has no ack); handler_ri_exchange_direct_resolve_body_test.go:77 |
| cloud_check.checked_region | validated equal to the row's region (direct_resolve_request.go:83); in the note (:100) | reaches the target (as part of the stored note) | handler_ri_exchange_direct_resolve_test.go:120 |
| cloud_check.checked_at | RIExchangeDirectResolve.CheckedAt (handler_ri_exchange_direct_resolve.go:206, parsed at direct_resolve_request.go:86); compared with the DB clock and earliest bound (store :130-136); in the note (:100) | reaches the target | handler_ri_exchange_direct_resolve_test.go:116, :137 |

## Ladder

### B6: ladderResolveRequest -> resolveLadderTranche (POST /api/ladder/tranches/{id}/resolve)

- Request type: ladderResolveRequest (+ ladderResolveCloudCheck), handler_ladder_resolve.go:39-52. Decode: decodeLadderResolve, :148-166 (site :155; auxiliary EOF check :158).
- Target type: config.LadderResolveFailed (internal/config/store_postgres_ladder_resolve.go:38-48) and config.LadderCloudCheck (:27-32). Converters: body.cloudCheck (handler_ladder_resolve.go:195-208, L1) and body.statusReason (:213-225, L1), built at applyLadderResolveFailed :127-130. Store: ResolveLadderTrancheFailed (:88, L2) -> TransitionLadderTrancheTx (store_postgres_ladder_lifecycle.go:66-83, L3, sets ladder_tranches.status_reason).
- Per-pair disposition: matches (status_reason is built from every free-text and check field; the store gates on the acknowledge flag and the check).

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| outcome | none: decides the branch ("completed" -> 501 at handler_ladder_resolve.go:92-94; "failed" proceeds); validated :170-172 | validation or authorization only | handler_ladder_resolve_test.go:114,147 |
| reason | LadderResolveFailed.StatusReason via statusReason (:216,219) -> ladder_tranches.status_reason (store_postgres_ladder_lifecycle.go:78) | reaches the target | handler_ladder_resolve_test.go:154 (asserts the exact string passed to the fixture store) |
| note | StatusReason suffix (:221-223) -> status_reason | reaches the target | handler_ladder_resolve_test.go:154 |
| acknowledge_unknown_outcome | LadderResolveFailed.Acknowledge (:129); used by the store only as a gate (validateLadderUnverifiedFail, store_postgres_ladder_resolve.go:184, refusing an unproven chain without it); also logged (:142). The value itself is not written. Must be sent together with cloud_check (:182) | validation or authorization only | handler_ladder_resolve_test.go:114 ("ack without check"), :154 |
| cloud_check | LadderResolveFailed.Check non-nil (:129, :207); gates the unverified path and selects the "unverified" status_reason form (:218) | reaches the target | handler_ladder_resolve_test.go:154 |
| cloud_check.checked_account_id | LadderCloudCheck.AccountID; store checks it names the tranche's account (store_postgres_ladder_resolve.go:195) and the value is rendered into status_reason (handler_ladder_resolve.go:218) | reaches the target | handler_ladder_resolve_test.go:154 |
| cloud_check.checked_region | LadderCloudCheck.Region; format-checked only in the handler (:204); not checked by the store; reaches the store solely through status_reason (:218) | reaches the target | handler_ladder_resolve_test.go:154 |
| cloud_check.checked_service | LadderCloudCheck.Service; store requires it equals the tranche layer (:197) and it is rendered into status_reason (:218) | reaches the target | handler_ladder_resolve_test.go:154 |
| cloud_check.checked_at | LadderCloudCheck.CheckedAt (parsed :200); store checks not in the future and after the chain's last change (:199-202); rendered into status_reason (:218-219) | reaches the target | handler_ladder_resolve_test.go:154 (RFC3339 offset normalized to UTC) |

Direct decodes in the ladder area, none of which is an adapter (each is decoded straight into a store or engine struct, so there is no per-field copy to drop):

- `internal/api/handler_ladder.go:102` decodes into `config.LadderConfigDB` (strict decoder, reason in the comment at `:96-98`). `created_at` and `updated_at` are accepted but the upsert writes `NOW()` (`internal/config/store_postgres_ladder.go:105,118`), with no source comment saying so. `id` is written only when a new row is inserted and is ignored on conflict (`:89-91,106-118`).
- `internal/api/handler_ladder_amend.go:86` decodes into `config.LadderAmendment` and passes it whole to `AmendLadderTranche`; all three fields (`expected_revision`, `scheduled_date`, `amount_usd_hr`) are used (`internal/config/store_postgres_ladder_amend.go:94,97,105-107`).
- `ladderTimelineRequest` (`internal/api/handler_ladder_timeline.go:16-21`) is a scope struct filled from query-string parameters and has no decode site, so it is not an API body.
- Second hop of the ladder config upsert: `ladderConfigToEngine` (`internal/server/handler_ladder.go:581-612`) forwards mode, cadence, ramp_schedule, target_coverage, buffer_fraction, baseline_percentile, lookback_days, max_actions_per_run, buffer_utilization_threshold and max_hourly_commit_per_run (the spend cap) (`:601-610`). `enabled` is consumed before the conversion (`:221`) and `cloud_account_id` becomes the account argument (`:227-240`). `provider` is not forwarded: the engine scope is hard-coded to AWS (`:598`). This is a store-to-engine conversion, not an API decode.
