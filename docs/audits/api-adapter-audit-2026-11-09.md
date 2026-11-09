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

## Purchases/revoke

### E8: executionSettleRequest via settleExecution

- Request type: executionSettleRequest, handler_execution_settle.go:26-29.
- Decode site: handler_execution_settle.go:142 (appendix row 10); EOF check :145 auxiliary (row 11); decoder function decodeExecutionSettle :135-159.
- Target type: config.ExecutionSettleRequest (internal/config/store_postgres_execution_settle.go:30-35).
- Converting function: the struct literal at handler_execution_settle.go:70-72.
- Path: handler (0), store.SettleFailedExecution (store_postgres_execution_settle.go:72-110) (2), the UPDATE purchase_executions at :101-104 with the record built by settlementRecord (:238-241) (3).
- Final disposition: matches.

| API json tag | Target | Disposition | Covering test |
| --- | --- | --- | --- |
| reason | ExecutionSettleRequest.Reason (:71); stored inside the outcome_settlement text by settlementRecord (store :240, SQL arg :104) | reaches the target | handler_execution_settle_integration_test.go:55 (response carries the reason; the stored record is the same string) |
| acknowledge_unknown_outcome | ExecutionSettleRequest.Acknowledge (:71); the store refuses false (store :114-115) and records the constant `ack=true` (store :239) | reaches the target (FORWARDED wins over the handler's own check at :149) | handler_execution_settle_test.go:20-21; handler_execution_settle_integration_test.go:88 |

### E9: ExecutePurchaseRequest via executePurchase

- Request type: ExecutePurchaseRequest, handler_purchases.go:3145-3167, holding `[]config.RecommendationRecord` (internal/config/types.go:640-781).
- Decode site: handler_purchases.go:3233 inside parseExecutePurchaseBody :3231-3247 (appendix row 13).
- Target type: config.PurchaseExecution (store write SavePurchaseExecutionTx, internal/config/store_postgres.go:1143-1274).
- Converting functions: priceAndEnforcePurchaseConstraints (purchase_pricing.go:19-24) rewrites each rec in place through priceRecommendationsFromStore (:33-78) and priceFromStored (:131-157); newPendingExecution (handler_purchases.go:3771-3789) copies Recommendations and CapacityPercent.
- Path: converters (1), persistExecutionAndSuppressions (handler_purchases.go:3635-3672) (2), SavePurchaseExecutionTx INSERT (store_postgres.go:1176-1267; recommendations marshalled whole at :1160, capacity_percent at :1167-1170 and :1254) (3).
- Rule for every `recommendations[]` element: after validation and identity matching, priceFromStored copies the STORED row (`out := *stored`, purchase_pricing.go:148) and overlays only `count`, `recommended_count` and `selected` from the request (:149-151) and scaled cost fields from the stored row (:152-155). The source comment that says so: handler_purchases.go:3146-3150 ("Only count, recommended_count and selected are taken from the request; id, details and every cost field are replaced from the stored row") and purchase_pricing.go:124-128. Rows that cite this rule are marked "deliberately ignored (rule R)".
- Identity matching: the request's provider, cloud_account_id, service, region, resource_type, engine, term, payment, availability_zone and the Details-derived platform, tenancy and az_config select which stored row is bought (config.RecIdentityKey, internal/config/recommendation_identity.go:98-116; match at purchase_pricing.go:43-48). The matched value is not what is written; the stored row is.
- Final disposition: matches (deliberate).

Top-level fields:

| API json tag | Target | Disposition | Covering test |
| --- | --- | --- | --- |
| recommendations | PurchaseExecution.Recommendations (handler_purchases.go:3781), JSONB column recommendations (store_postgres.go:1160, arg :1244); each element per the rows below | reaches the target (re-priced from the stored set) | purchase_pricing_test.go:131-198 |
| capacity_percent | PurchaseExecution.CapacityPercent (:3787), column capacity_percent (store_postgres.go:1167-1170, arg :1254); normalized by normalizeCapacityPercent (:3364-3372); cross-checked with recommended_count (validation.go:728-743); in the submit idempotency key (handler_purchases.go:3686) | reaches the target | none for the persisted value (purchase_pricing_test.go:163 sends capacity_percent 50 but never asserts saved.CapacityPercent); the dedupe key's capacity component: handler_purchases_dedupe_test.go:54 |
| execute_mode | selects the direct-execute branch (handler_purchases.go:3850) after authorizeSessionExecuteDirect (:1708); not stored | validation or authorization only (decides the path; any value other than "direct" is the default flow, documented at :3158-3162) | none found for the direct branch (handler_per_account_perms_test.go:610 covers the account gate only) |

`recommendations[]` elements (dotted path `recommendations[].<tag>`, struct config.RecommendationRecord):

| API json tag | Target | Disposition | Covering test |
| --- | --- | --- | --- |
| recommendations[].id | lookup key for the originally selected stored row (purchase_pricing.go:49-67, checkPurchaseDetailIdentity); the written ID is the matched stored row's | validation or authorization only | purchase_pricing_test.go:915 (stale origin id refused), :171 (stored id written) |
| recommendations[].provider | identity key (config.RecIdentityKey recommendation_identity.go:112); canonicalized by validatePurchaseRecommendation (validation.go:632-637); validatePurchaseRecommendationScope runs on cloud_account_id only | validation or authorization only (selects the stored row) | purchase_pricing_test.go:203, :571 |
| recommendations[].service | identity key; validation (validation.go:638-641) | validation or authorization only | purchase_pricing_test.go:203, :571 |
| recommendations[].region | identity key | validation or authorization only | purchase_pricing_test.go:203, :571 |
| recommendations[].resource_type | identity key | validation or authorization only | purchase_pricing_test.go:203, :571 |
| recommendations[].engine | identity key | validation or authorization only | purchase_pricing_test.go:571 |
| recommendations[].details | identity components platform, tenancy, az_config (ComputeIdentity, IdentityAZConfig); refuse-only comparison of tenancy, platform, scope, memory_gb, az_config with the stored match when id is empty (purchase_pricing.go:227-244); never copied (comment at purchase_pricing.go:219-226) | deliberately ignored for the written value, cited at handler_purchases.go:3146-3150 and purchase_pricing.go:219-226; used for validation as described | purchase_pricing_test.go:180 (stored details written), :1001, :1021, :1072, :1118 |
| recommendations[].availability_zone | identity key component (recommendation_identity.go:114) and mismatch check (purchase_pricing.go:316) | validation or authorization only | purchase_pricing_test.go:571 |
| recommendations[].count | RecommendationRecord.Count = req.Count (purchase_pricing.go:149); costs scaled by it (:147-155); validated > 0 (validation.go:642-644) | reaches the target | purchase_pricing_test.go:172 |
| recommendations[].recommended_count | out.RecommendedCount = req.RecommendedCount (:150); checked by validateCapacityConsistency (validation.go:728-743) | reaches the target | purchase_pricing_test.go:173 |
| recommendations[].term | identity key; validated to 1 or 3 (validation.go:645-647) | validation or authorization only | purchase_pricing_test.go:398, :571 |
| recommendations[].payment | identity key (lowercased); normalized and validated by validatePurchaseRecommendation (validation.go:656-693); a schedule-changing coercion is reported back as payment_adjustments | validation or authorization only (selects the stored variant) | purchase_pricing_test.go:398 |
| recommendations[].upfront_cost | replaced by stored.UpfrontCost * ratio (purchase_pricing.go:152) | deliberately ignored (rule R; also handler_purchases.go:3219-3221) | purchase_pricing_test.go:174 |
| recommendations[].monthly_cost | read once to refuse a negative value (validation.go:653-655); then replaced by scaledCost(stored.MonthlyCost) (purchase_pricing.go:154) | deliberately ignored for the written value (rule R); validation use as stated | purchase_pricing_test.go:176 |
| recommendations[].savings | replaced by stored.Savings * ratio (purchase_pricing.go:153) | deliberately ignored (rule R) | purchase_pricing_test.go:177 |
| recommendations[].on_demand_cost | replaced by scaledCost(stored.OnDemandCost) (purchase_pricing.go:155) | deliberately ignored (rule R) | purchase_pricing_test.go:179 |
| recommendations[].commitment_currency | replaced by the stored value (`out := *stored`, :148) | deliberately ignored (rule R) | none |
| recommendations[].savings_percentage | replaced by the stored value | deliberately ignored (rule R) | none |
| recommendations[].selected | out.Selected = req.Selected (:151) | reaches the target | purchase_pricing_test.go:181 |
| recommendations[].purchased | replaced by the stored value | deliberately ignored (rule R) | none |
| recommendations[].purchase_id | replaced by the stored value | deliberately ignored (rule R) | none |
| recommendations[].error | replaced by the stored value | deliberately ignored (rule R) | none |
| recommendations[].cloud_account_id | identity key (nil and empty collapse, recommendation_identity.go:107-110); validatePurchaseRecommendationScope authorization (scoping.go:179-203); purchaseConstraintSets AccountIDs after replacement (handler_purchases.go:3306-3308) | validation or authorization only | handler_per_account_perms_test.go:610-719; purchase_pricing_test.go:249 |
| recommendations[].source_account_id | replaced by the stored value | deliberately ignored (rule R) | none |
| recommendations[].suppressed_count | replaced by the stored value | deliberately ignored (rule R) | none |
| recommendations[].suppression_expires_at | replaced by the stored value | deliberately ignored (rule R) | none |
| recommendations[].primary_suppression_execution_id | replaced by the stored value | deliberately ignored (rule R) | none |
| recommendations[].usage_history | replaced by the stored value | deliberately ignored (rule R) | none |
| recommendations[].vcpu | replaced by the stored value (not persisted anyway, types.go:767-778) | deliberately ignored (rule R) | none |
| recommendations[].memory_gb | replaced by the stored value; the Details memory_gb is the compared discriminator (purchase_pricing.go:320) | deliberately ignored (rule R) | purchase_pricing_test.go:757 (details discriminator) |

### N1: revokeConfirmBody via revokePurchase (not an adapter)

- Request type: revokeConfirmBody, handler_purchases_revoke.go:74-77. Decode site: :296 (appendix row 12).
- Why not an adapter: the decoded struct is passed whole to dispatchProviderRevoke (:301, :463) and callAzureReturn (:843-886) and used only to compare the user's confirmed quote with Azure's fresh quote (checkConfirmedRefund :1016-1034). No field is copied into a different struct or into a store or SDK call argument; the Return request is built from the fresh CalculateRefund session (postAzureReturn :899-915).
- Path facts: the body is decoded only on the completed-purchase path (loadAndRevokePurchaseHistory :294-299); the scheduled-execution path (revokeScheduledExecution :317) never reads it; AWS and GCP records are decoded and then refused with 422 (:467-472).

| API json tag | Use | Disposition | Covering test |
| --- | --- | --- | --- |
| expected_refund_amount | required for Azure (callAzureReturn :859); compared with Azure's quote within revokeQuoteEpsilon (:1027) | validation or authorization only | handler_purchases_revoke_test.go:1140 (divergence refused), :1165 (within epsilon succeeds) |
| expected_refund_currency | required for Azure (:859); compared case-insensitively with the quote currency (:1021) | validation or authorization only | handler_purchases_revoke_guard_test.go:97-100 |

### B7: MarketplaceListRequest -> marketplaceList (POST /api/purchases/{id}/marketplace-list)

- Request type: MarketplaceListRequest + MarketplacePriceTier, internal/api/handler_marketplace.go:156-170. Decode: validateMarketplaceListRequest, :226-230 (site :227).
- Target: ec2MarketplaceListingRequest.PriceSchedule (alias of ec2svc.MarketplaceListingRequest, provider_wiring.go:36-38) sent to AWS CreateReservedInstancesListing, and the persisted purchase_history.listing_price_schedule. Converters: resolveMarketplacePriceSchedule (handler_marketplace.go:595-635, passes a valid supplied schedule through unchanged, else builds a default tier) and toAWSPriceSchedule (handler_marketplace_attempt.go:80-86). Store: ClaimMarketplaceListingSlot (store_postgres.go:2547; UPDATE sets listing_price_schedule at :2563-2564). Outbound: createAndPersistListing (handler_marketplace_attempt.go:198-204) -> ec2svc client in the external module `cloud-commitments-go`.
- Retry note: on a resumed attempt the persisted schedule replaces the request's (handler_marketplace_attempt.go:129-141); this is documented at :93-94 and :4-9 (see appendix site handler_marketplace_attempt.go:133).
- Per-pair disposition: matches.

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| price_schedule | when present and valid: passed through whole to ClaimMarketplaceListingSlot and CreateMarketplaceListing; when absent or empty: the handler computes a one-tier default (handler_marketplace.go:614-634), the documented behavior (struct comment :164-167). Floors and range checks are validation (:560-583,:599-611) | reaches the target (as a whole value; per-tier fields below) | handler_marketplace_attempt_test.go:441 |
| price_schedule[].term_months | ec2MarketplacePriceTier.Term (handler_marketplace_attempt.go:83) -> ec2MarketplaceListingRequest.PriceSchedule (:202); persisted copy in listing_price_schedule (store_postgres.go:2563-2564) | reaches ec2svc.MarketplaceListingRequest.PriceSchedule at the client call (handler_marketplace_attempt.go:199) (store write not traced: the outbound SDK field is inside the external module cloud-commitments-go; the persisted JSON copy is traced to the SQL column) | handler_marketplace_attempt_test.go:441 (stored JSON decoded back equals the tiers the fake EC2 client received); :255 |
| price_schedule[].price | ec2MarketplacePriceTier.Price (handler_marketplace_attempt.go:83), same path | reaches ec2svc.MarketplaceListingRequest.PriceSchedule at the client call (store write not traced: external module cloud-commitments-go; persisted copy traced) | handler_marketplace_attempt_test.go:441; :255 |

### T1: anonymous approval-token body {token} -> approvePurchase (POST /api/purchases/approve/{id})

- Request type: anonymous struct with one field Token tagged json:"token" at internal/api/router.go:605-607 (decode at :608), inside `resolveApprovalToken` (router.go:602). JSON is tried first, then a form body (`url.ParseQuery`, router.go:611-617, not a decode site), then the query string (:619). The same helper serves 4 routes (router.go:168-176), so each route is its own pair below.
- Converter: `resolveApprovalToken` router.go:602 (returns the string); caller `approvePurchaseHandler` router.go:622-628 passes it as positional argument 4 of `(*Handler).approvePurchase` (handler_purchases.go:1001, call at router.go:627).
- Target: positional argument `token` of approvePurchase; no struct.

| API json tag | target Go field | disposition | covering test |
| --- | --- | --- | --- |
| `token` | positional argument 4 (`token`) of `approvePurchase` -> `approveViaToken` handler_purchases.go:1046/:1137 -> `purchase.Manager.ApproveExecution` (handler_purchases.go:1224) -> `ValidateApprovalToken` (approvals.go:74) and pinned into the status-claim UPDATE (`claimRunningForApprove` claim.go:55 -> `TransitionExecutionStatusTokenPinned` store_postgres.go:1384 -> predicate `approval_token_hash = $7`, store_postgres.go:1438); delayed branch: `ValidateApprovalToken` handler_purchases.go:1182, then `approveWithDelay` handler_purchases.go:1185 -> `ScheduleApprovedExecution` with the token pinned (handler_purchases.go:1589-1595) | reaches the target (FORWARDED into the store write as a compare-only CAS predicate; it is never persisted as a column; see judgment note J1). When the session is RBAC-authorized the token is ignored on purpose, source comment handler_purchases.go:1015-1019 | body decode: handler_purchases_test.go:4505 (`TestResolveApprovalToken_PostBodyTakesPriority`), :4547; token pin: internal/purchase/token_rotation_pin_integration_test.go:74, internal/api/handler_purchases_token_errors_test.go:170, store_postgres_db_test.go:1819 |

Pair disposition: matches.

### T2: anonymous approval-token body {token} -> cancelPurchase (POST /api/purchases/cancel/{id})

- Request type, converter: as T1 (router.go:605-608, :602). Caller `cancelPurchaseHandler` router.go:637-643 -> `(*Handler).cancelPurchase` handler_purchases.go:1754 (call at router.go:642).

| API json tag | target Go field | disposition | covering test |
| --- | --- | --- | --- |
| `token` | positional argument 4 (`token`) of `cancelPurchase` -> `cancelViaToken` handler_purchases.go:1817/:1826 -> `Manager.CancelExecution` (handler_purchases.go:1831; approvals.go:516) -> `loadCancelableExecution` `ValidateApprovalToken` (approvals.go:656) and `CancelExecutionAtomicTokenPinned` (approvals.go:540; store_postgres.go:1540 -> UPDATE predicate `approval_token_hash = $4`, store_postgres.go:1559) | reaches the target (compare-only CAS predicate, never persisted; judgment note J1). With an RBAC-authorized session the token is ignored on purpose, source comment handler_purchases.go:1781 ("the token in the URL is informational at that point") | body decode: handler_purchases_test.go:4505, :4533, :4547; token pin: internal/purchase/token_rotation_pin_integration_test.go:109, store_postgres_token_pin_cancel_db_test.go:63 |

Pair disposition: matches.

### T3: anonymous approval-token body {token} -> revokeViaEmailToken (POST /api/purchases/revoke/{execID})

- Request type, converter: as T1. Caller `revokeViaEmailTokenHandler` router.go:656-662 -> `(*Handler).revokeViaEmailToken` handler_purchases.go:1983 (call at router.go:661). The GET route (router.go:175) reads the token from the query string only (resolveApprovalToken decodes JSON only for POST), so it is not an adapter for this decode.

| API json tag | target Go field | disposition | covering test |
| --- | --- | --- | --- |
| `token` | positional argument 4 (`token`) of `revokeViaEmailToken` -> `revokeViaToken` handler_purchases.go:2023/:2030 -> `validateRevokeToken` :2035/:2149 -> `ValidateApprovalToken` | validation or authorization only (compared with the stored hash; `revokeViaSession` is then called without the token, handler_purchases.go:2041). Also decides the branch: `fallsThroughToToken` in `tryRevokeViaSession` (handler_purchases.go:2087) | handler_purchases_test.go:6427 (`TestRevokePOSTReadsTokenFromBody`), :6468 (`TestRevokePurchase_RejectsRotatedToken`) |

Pair disposition: matches.

### T4: anonymous approval-token body {token} -> getApprovalDetails (POST /api/purchases/approve/{id}/details)

- Request type, converter: as T1. Caller `approvalDetailsHandler` router.go:630-635 -> `(*Handler).getApprovalDetails` handler_purchases_approval_details.go:30 (call at router.go:634). This route performs no write.

| API json tag | target Go field | disposition | covering test |
| --- | --- | --- | --- |
| `token` | positional argument 4 (`token`) of `getApprovalDetails` -> `authorizeApprovalDetailsRead` handler_purchases_approval_details.go:40/:50 -> `purchase.ValidateApprovalToken` :59 | validation or authorization only (read endpoint; nothing is persisted or forwarded) | handler_purchases_approval_details_test.go:117 (`TestHandleRequest_ApprovalDetails_TokenAndContactBothRequired`) |

Pair disposition: matches.

#### Judgment note J1

Under the schema PRECEDENCE rule ("reaches the target wins whenever the value is FORWARDED, i.e. used by the callee's store write"), the token in T1 and T2 is used inside the store UPDATE (as the WHERE predicate that pins the token hash), so it is classed 'reaches the target'. An equally defensible reading is 'validation or authorization only', because the value is compared, never written. Either reading yields no DROPPED row; the choice only changes the label.

## Overrides

### O1: AccountServiceOverrideRequest -> saveAccountServiceOverride (PUT /api/accounts/:id/service-overrides/:provider/:service)

- Area: overrides. Request type: AccountServiceOverrideRequest, handler_accounts.go:68. Decode: handler_accounts.go:1193; key-presence auxiliary decode `presentKeys` at internal/api/handler_config.go:389 (called :1198).
- Target: config.AccountServiceOverride, internal/config/types.go:1533. Converter: buildServiceOverride, handler_accounts.go:1245 (overlayOverrideScalars :1262, overlayOverrideSlices :1280), run inside the store's locked transaction via mergeAndValidateServiceOverride :1228. Store: UpdateAccountServiceOverrideAtomic, internal/config/store_postgres_config_audit.go:156 -> writeOverrideAudited :201 -> upsertAccountServiceOverrideReturning :212 (SQL :220-235). Semantics: a key present in the body replaces the stored value, null clears it, an absent key keeps it (comment :1241-1244).
- Final disposition: fixed in #1788 (matches).
- Guards: handler_override_merge_test.go:193 (every json field is merged, request and stored sides), the per-key table at :59, single-key test :78, null-clears test :94.

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| enabled | AccountServiceOverride.Enabled (:1263-1265) -> SQL `enabled = $5` (store_postgres_config_audit.go:224, arg :231) | reaches the target | handler_override_merge_test.go:193, :78 |
| term | .Term (:1266-1268) -> `term = $6`; also checked with payment on the merged row (combos.check :1230) | reaches the target | handler_override_merge_test.go:193, :78, :170 |
| payment | .Payment (:1269-1271) -> `payment = $7`; same combo check | reaches the target | handler_override_merge_test.go:193, :78, :170 |
| coverage | .Coverage (:1272-1274) -> `coverage = $8` | reaches the target | handler_override_merge_test.go:193, :78 |
| ramp_schedule | .RampSchedule (:1275-1277) -> `ramp_schedule = $9` | reaches the target | handler_override_merge_test.go:193, :78 |
| include_engines | .IncludeEngines (:1281-1283) -> `include_engines = $10` | reaches the target | handler_override_merge_test.go:193, :78 |
| exclude_engines | .ExcludeEngines (:1284-1286) -> `exclude_engines = $11` | reaches the target | handler_override_merge_test.go:193, :78 |
| include_regions | .IncludeRegions (:1287-1289) -> `include_regions = $12` | reaches the target | handler_override_merge_test.go:193, :78 |
| exclude_regions | .ExcludeRegions (:1290-1292) -> `exclude_regions = $13` | reaches the target | handler_override_merge_test.go:193, :78 |
| include_types | .IncludeTypes (:1293-1295) -> `include_types = $14` | reaches the target | handler_override_merge_test.go:193, :78 |
| exclude_types | .ExcludeTypes (:1296-1298) -> `exclude_types = $15` | reaches the target | handler_override_merge_test.go:193, :78 |

### B8: OverrideImportRequest -> importAccountServiceOverrides control flow (POST /api/accounts/service-overrides/import)

- Request type: OverrideImportRequest, handler_override_import.go:51-56. Decode: parseOverrideImportRequest, :128-144 (site :130; shared with B9).
- Targets: OverrideImportReport.Mode / DryRun (handler_override_import.go:114) and the import audit event details (overrideImportEvent :370-381 -> SaveConfigAuditEventTx, store_postgres_config_audit.go:297-321, config_audit_events.details).
- Per-pair disposition: matches.

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| mode | OverrideImportReport.Mode (:114) and audit event details.mode (:371) -> config_audit_events.details; also decides strict vs best_effort (:115) and defaults to "strict" when empty (:133-135) | reaches the target | handler_override_import_test.go:105,119; handler_override_import_integration_test.go:22 (Postgres) |
| dry_run | OverrideImportReport.DryRun (:114); control flow: true skips applyOverrideImport (:118-120). Not stored | validation or authorization only (control flow) | handler_override_import_test.go:146 |
| overrides | rows of B9 (`overrides[].*`), bounded 1..5000 (:157-159); exactly one of overrides or csv (:139) | reaches the target (as a whole value; see B9) | handler_override_import_test.go:33,74 |
| csv | decoded by decodeOverridesCSV (override_transfer_csv.go:117-142) into the same B9 rows | reaches the target (as a whole value; see B9) | handler_override_import_test.go:33 (csv format), :183 |

### B9: OverrideTransferRow -> config.AccountServiceOverride (import apply)

- Request type: OverrideTransferRow, internal/api/override_transfer.go:21-40, from `overrides[]` (JSON, site handler_override_import.go:130) or from CSV text (decodeOverridesCSV, override_transfer_csv.go:117, no decode site of its own).
- Target type: config.AccountServiceOverride (built at handler_override_import.go:343; fields applied by applyOverrideFieldsTo, override_transfer.go:116-121). Account resolved by overrideAccountIndex.resolve (override_account_index.go:50-69). Store: applyOverrideImport (L1, :330-359) -> SaveAccountServiceOverrideAuditedTx (L2, store_postgres_config_audit.go:139) -> upsertAccountServiceOverrideReturning (L3, :212-243, columns :222-233).
- Per-pair disposition: matches (the informational fields are documented as ignored).
- The comment at override_transfer.go:14-17 states "AccountName, CreatedAt and UpdatedAt are informational: import ignores them" and that an unset field clears that field.

| API json tag (dotted) | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| overrides[].account_id | selects the account: overrideAccountIndex.resolve (:54-55); the written AccountID is the resolved in-scope account's own id (handler_override_import.go:343) | validation or authorization only (account resolution and scope; out-of-scope ids read as unknown) | handler_override_import_test.go:56,105 |
| overrides[].account_name | none | deliberately ignored, source comment override_transfer.go:14-16 | handler_override_import_test.go:33 (round trip) |
| overrides[].account_external_id | fallback account key and consistency check (override_account_index.go:57-68,77-78) | validation or authorization only | handler_override_import_test.go:56 |
| overrides[].provider | AccountServiceOverride.Provider (handler_override_import.go:343) -> account_service_overrides.provider; validated (override_account_index.go:51-53) | reaches the target | handler_override_import_test.go:33,105 |
| overrides[].service | AccountServiceOverride.Service (:343) -> service; validated (:227-229) | reaches the target | handler_override_import_test.go:33,105 |
| overrides[].enabled | AccountServiceOverride.Enabled via applyOverrideFieldsTo (override_transfer.go:117) -> enabled (store_postgres_config_audit.go:224) | reaches the target | handler_override_import_test.go:74,33 |
| overrides[].term | Term (:117) -> term | reaches the target | handler_override_import_test.go:56,74 |
| overrides[].payment | Payment (:117) -> payment | reaches the target | handler_override_import_test.go:74 |
| overrides[].coverage | Coverage (:117) -> coverage (DECIMAL(5,2), validated :265) | reaches the target | handler_override_import_test.go:33 |
| overrides[].ramp_schedule | RampSchedule (:117) -> ramp_schedule (length-bounded :257-260) | reaches the target | handler_override_import_test.go:259 |
| overrides[].include_engines | IncludeEngines (:118) | reaches the target | handler_override_import_test.go:33 |
| overrides[].exclude_engines | ExcludeEngines (:118) | reaches the target | handler_override_import_test.go:33 |
| overrides[].include_regions | IncludeRegions (:119) | reaches the target | handler_override_import_test.go:33 |
| overrides[].exclude_regions | ExcludeRegions (:119) | reaches the target | handler_override_import_test.go:33 |
| overrides[].include_types | IncludeTypes (:120) | reaches the target | handler_override_import_test.go:33 |
| overrides[].exclude_types | ExcludeTypes (:120) | reaches the target | handler_override_import_test.go:33 |
| overrides[].created_at | none (an update keeps the stored CreatedAt, handler_override_import.go:345; an insert stamps now, :343) | deliberately ignored, comment override_transfer.go:14-16 | handler_override_import_test.go:33 |
| overrides[].updated_at | none | deliberately ignored, comment override_transfer.go:14-16 | handler_override_import_test.go:33 |

The CSV form decodes the same columns; overrideRowFromCSV (override_transfer_csv.go:163-189) leaves account_name, created_at and updated_at unset, so the CSV columns for them are accepted by the header check (:144-161) and ignored, the same documented behavior.

## Auth/profile

Common facts used below:

- Handler to service for U1-U10 goes through the interface `AuthServiceInterface` (internal/api/types.go:195); in production the implementation is `authServiceAdapter` (internal/server/app.go:1169). Where the adapter copies a struct (Login :1179-1184, SetupAdmin :1218-1221, ConfirmPasswordReset :1248-1251) every field is copied; the other methods pass positional arguments straight through.
- Passwords arrive base64-encoded and are decoded in the handler (`decodeBase64Password`) before the call; that is a transform, not a drop.
- Trace depth used for the auth pairs: handler = 0, adapter = level 1, auth.Service method = level 2, auth store method = level 3.

Not adapters at the handler (decoded straight into the auth package's own wire type and passed whole to the service; the service-side conversion lives in internal/auth/service_api.go, out of scope for this audit and covered by #923): handler_apikeys.go:63, handler_users.go:48 and :168, handler_groups.go:50 and :123. Observations only, read while checking the call, not table rows: `CreateUserAPI` copies email, password, groups (internal/auth/service_api.go:256-260); `UpdateUserAPI` copies groups, email when non-empty, active (:276-288); `CreateGroupAPI` copies name, description, permissions, allowed_accounts (:353-358); `UpdateGroupAPI` applies name, description, permissions, allowed_accounts via applyUpdateGroupRequest (:371-384). `CreateAPIKeyAPI` (internal/auth/service_apikeys_api.go:199) was not traced.

### U1: LoginRequest -> login (POST /api/auth/login)

- Area: auth/profile. Request type: LoginRequest, internal/api/types.go:302. Decode: handler_auth.go:30. Call: `h.auth.Login(ctx, loginReq)` :48. Converter: authServiceAdapter.Login, internal/server/app.go:1178 (copies all four fields into auth.LoginRequest :1179-1184; target internal/auth/types.go:254). Service: Service.Login internal/auth/service.go:150; store lookups GetUserByEmail (service.go:190) and, for the token form, loginWithMFAToken (internal/auth/service_mfa_login_token.go:77-99).
- Final disposition: matches (the issue #19 trigger class, now guarded for mfa_token).

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| email | auth.LoginRequest.Email (app.go:1180); normalized service.go:157-161; lookup key GetUserByEmail :190 / claimMFALoginToken (service_mfa_login_token.go:110) | validation or authorization only | internal/server/adapter_mfa_token_test.go:23; adapter_test.go:48 |
| password | auth.LoginRequest.Password (app.go:1181); verifyPassword service.go:247; must be empty in the token form (service_mfa_login_token.go:78) | validation or authorization only | internal/server/adapter_mfa_token_test.go:23 |
| mfa_code | auth.LoginRequest.MFACode (app.go:1182); verifyLoginMFACode service.go:265 | validation or authorization only | internal/server/adapter_mfa_token_test.go:23 |
| mfa_token | auth.LoginRequest.MFAToken (app.go:1183); hashed lookup and consume service_mfa_login_token.go:81-98 | validation or authorization only | internal/server/adapter_mfa_token_test.go:23 |

### U2: SetupAdminRequest -> setupAdmin (POST /api/auth/setup-admin)

- Area: auth/profile. Request type: SetupAdminRequest, types.go:331. Decode: handler_auth.go:374. Call: `h.auth.SetupAdmin` :387. Converter: authServiceAdapter.SetupAdmin, app.go:1217 (copies both fields :1218-1221 into auth.SetupAdminRequest, internal/auth/types.go:321). Service: Service.SetupAdmin internal/auth/service_user.go:19. Store: CreateAdminIfNone internal/auth/store_postgres_admin.go:159 (args via userInsertArgs internal/auth/store_postgres.go:188-200).
- Final disposition: matches.

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| email | auth.SetupAdminRequest.Email (app.go:1219) -> normalizeAccountEmail service_user.go:28 -> User.Email :49 -> users email arg store_postgres.go:199 | reaches the target | internal/server/adapter_test.go:621 (round trip) |
| password | auth.SetupAdminRequest.Password (app.go:1220) -> hashPassword service_user.go:38 -> User.PasswordHash :50 -> arg store_postgres.go:200 (stored as a hash, never plain) | reaches the target (hashed) | internal/server/adapter_test.go:621 |

### U3: PasswordResetRequest -> forgotPassword (POST /api/auth/forgot-password)

- Area: auth/profile. Request type: PasswordResetRequest, types.go:338. Decode: handler_auth.go:414. Call: `h.auth.RequestPasswordReset(ctx, pwdReq.Email)` :435 (positional argument 2). Adapter: app.go:1243 passes it through. Service: internal/auth/service_password.go:338.
- Final disposition: matches (lookup key; the email is sent to the STORED user email, service_password.go:389, never to the request value).

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| email | positional argument 2 of RequestPasswordReset -> store GetUserByEmail (service_password.go:339); also the per-email rate-limit bucket handler_auth.go:423 | validation or authorization only | handler_auth_test.go:536 (argument asserted at the handler boundary) |

### U4: PasswordResetConfirm -> resetPassword (POST /api/auth/reset-password)

- Area: auth/profile. Request type: PasswordResetConfirm, types.go:344. Decode: handler_auth.go:505. Call: `h.auth.ConfirmPasswordReset` :520. Converter: authServiceAdapter.ConfirmPasswordReset app.go:1247 (copies :1249-1250 into auth.PasswordResetConfirm, internal/auth/types.go:285). Service: service_password.go:415. Store: CompletePasswordReset internal/auth/store_postgres_credentials.go:61.
- Final disposition: matches.

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| token | auth.PasswordResetConfirm.Token (app.go:1249) -> validateResetToken service_password.go:416/:546 (hashed lookup) | validation or authorization only | handler_auth_test.go:595; adapter_test.go:139 (smoke, error path only) |
| new_password | auth.PasswordResetConfirm.NewPassword (app.go:1250) -> processPasswordReset service_password.go:575-595 -> hash -> `password_hash = $4` (store_postgres_credentials.go:63, arg :67) | reaches the target (hashed) | handler_auth_test.go:595 (handler boundary); no adapter-level test asserts it |

### U5: ProfileUpdateRequest -> updateProfile (PUT /api/auth/profile)

- Area: auth/profile. Request type: ProfileUpdateRequest, types.go:439. Decode: handler_auth.go:545. Call: `h.auth.UpdateUserProfile(ctx, session.UserID, profileReq.Email, currentPassword, newPassword)` :563 (positional arguments 3, 4, 5; decodeProfilePasswords :600). Adapter: app.go:1276 passes through. Service: Service.UpdateUserProfile internal/auth/service_user.go:793. Store: UpdateUserCredentials internal/auth/store_postgres_credentials.go:41.
- Final disposition: matches.

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| email | positional argument 3 -> updateUserEmail service_user.go:837 (empty or unchanged leaves the address as is) -> User.Email -> `email = $4` (store_postgres_credentials.go:43, arg :47) | reaches the target | handler_auth_test.go:835; adapter_test.go:320 (smoke, error path only) |
| current_password | positional argument 4 -> verifyPassword service_user.go:799 | validation or authorization only | handler_auth_test.go:835 |
| new_password | positional argument 5 -> updateUserPassword service_user.go:859-885 -> hash -> `password_hash = $5` (store_postgres_credentials.go:43, arg :47) | reaches the target (hashed) | handler_auth_test.go:835 |

### U6: ChangePasswordRequest -> changePassword (POST /api/auth/change-password)

- Area: auth/profile. Request type: ChangePasswordRequest, types.go:433. Decode: handler_auth.go:649. Call: `h.auth.ChangePasswordAPI(ctx, session.UserID, currentPassword, newPassword)` :659 (positional arguments 3, 4; decodeChangePasswordRequest :617). Adapter: app.go:1297. Service: ChangePasswordAPI internal/auth/service_api.go:309 -> ChangePassword service_password.go:254. Store: UpdateUserCredentials.
- Final disposition: matches.

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| current_password | positional argument 3 -> verifyPassword service_password.go:261 | validation or authorization only | handler_auth_test.go:926; adapter_test.go:214 (smoke) |
| new_password | positional argument 4 -> ChangePasswordRequest.NewPassword (service_api.go:310-313) -> hash service_password.go:278 -> UpdateUserCredentials :290 (`password_hash`) | reaches the target (hashed) | handler_auth_test.go:926 |

### U7: MFASetupRequest -> mfaSetup (POST /api/auth/mfa/setup)

- Area: auth/profile. Request type: MFASetupRequest, types.go:594. Decode: handler_auth.go:719. Call: `h.auth.MFASetupAPI(ctx, session.UserID, password)` :727 (positional argument 3). Adapter app.go:1302. Service: service_api.go:557 -> MFASetup internal/auth/service_mfa.go:318.
- Final disposition: matches.

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| password | positional argument 3 -> verifyPassword service_mfa.go:329 | validation or authorization only | handler_auth_test.go:1339 |

### U8: MFAEnableRequest -> mfaEnable (POST /api/auth/mfa/enable)

- Area: auth/profile. Request type: MFAEnableRequest, types.go:610. Decode: handler_auth.go:743. Call: `h.auth.MFAEnableAPI(ctx, session.UserID, body.Code)` :747 (positional argument 3). Adapter app.go:1306. Service: service_api.go:566 -> MFAEnable service_mfa.go:403.
- Final disposition: matches.

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| code | positional argument 3 -> validatePendingMFAEnrollment service_mfa.go:417; the counter derived from the code is recorded by EnableMFA (:432), the code itself is not stored | validation or authorization only | handler_auth_test.go:1376 |

### U9: MFADisableRequest -> mfaDisable (POST /api/auth/mfa/disable)

- Area: auth/profile. Request type: MFADisableRequest, types.go:622. Decode: handler_auth.go:764. Call: `h.auth.MFADisableAPI(ctx, session.UserID, password, body.Code)` :772 (positional arguments 3, 4). Adapter app.go:1310. Service: service_api.go:571 -> MFADisable service_mfa.go:501.
- Final disposition: matches.

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| password | positional argument 3 -> verifyPassword service_mfa.go:512 | validation or authorization only | handler_auth_test.go:1405 |
| code | positional argument 4 (`codeOrRecovery`) -> verifyMFAProof service_mfa.go:528 | validation or authorization only | handler_auth_test.go:1405 |

### U10: MFARegenerateRequest -> mfaRegenerateRecoveryCodes (POST /api/auth/mfa/regenerate-recovery-codes)

- Area: auth/profile. Request type: MFARegenerateRequest, types.go:630. Decode: handler_auth.go:789. Call: `h.auth.MFARegenerateRecoveryCodesAPI(ctx, session.UserID, body.Code)` :793 (positional argument 3). Adapter app.go:1314. Service: service_api.go:576 -> MFARegenerateRecoveryCodes service_mfa.go:543.
- Final disposition: matches.

| API json tag | Target Go field | Disposition | Covering test |
| --- | --- | --- | --- |
| code | positional argument 3 -> claimTOTPOrReject service_mfa.go:557 | validation or authorization only | handler_auth_test.go:1418 |

## Findings

### Dropped rows at the audited commit

| Pair | Field | Where | Status |
| --- | --- | --- | --- |
| R2 | `enabled` on registration approve | `internal/api/handler_registrations.go:303` overwrites the converter's value | fixed in #1905 |
| E5 | `utilization_threshold` 0 on PUT /api/ri-exchange/config | accepted at `handler_ri_exchange.go:3116`, stored as 95.0 at `internal/config/store_postgres.go:333-336` | fixed in #1906 |
| B1, B2 | `description` | `types.go:890-907` never reads it; `config.PurchasePlan` has no such field | open, #1776 |
| B2 | `target_accounts` on update | `mergeUpdatedPlan` (`handler_plans.go:431-469`) never reads it; the frontend uses PUT /api/plans/{id}/accounts | open, #1776 |
| B5 | `provider`, `service` in the service-config PUT body | overwritten from the URL path at `handler_config.go:510-514`, no comment | follow-up issue |

### 1. Fixed in this series

- Registration approve ignored an explicit `enabled` false (R2): fixed in #1905.
- An unknown `ramp_schedule` on `PlanRequest` fell back to `immediate` instead of being rejected (`types.go:919-923`, B1 and B2), and `utilization_threshold` 0 on PUT /api/ri-exchange/config was stored as 95 (E5): both fixed in #1906.
- #1775, an override save clearing the other fields of the override (O1): fixed in #1788, guarded by `TestAccountServiceOverrideRequest_EveryFieldIsMerged`.

### 2. Open

- #1776: `PlanRequest.description` is accepted and dropped on create and update; `target_accounts` is ignored on PUT /api/plans/{id}; the service-config `ramp_schedule` is ignored for an existing row (deliberate, source comment at `handler_config.go:349-350`, stale API spec and frontend comment); frontend-only fields. The `approveRegistration` item of that issue is fixed by #1905.
- #923: the `internal/auth` conversions behind the auth adapters (`CreateUserAPI`, `UpdateUserAPI`, `CreateGroupAPI`, `UpdateGroupAPI`, `CreateAPIKeyAPI`). This audit covers the handler and `internal/server` side only.

### 3. Tracked in the follow-up issue

The follow-up issue is #1907. It holds three checklists (pairs with no round-trip test, deliberately ignored fields with no source comment, dead fields) and these findings:

- **`PUT /api/config` still accepts `ri_exchange_utilization_threshold` 0.** `internal/config/validation.go:351-353` accepts 0 and the store saves it as 95 (`internal/config/store_postgres.go:333-336`). The comment at `validation.go:332-335` claims parity with the RI exchange route, which #1906 changed.
- **Dead request types and documentation drift.** `CreateUserRequest`, `UpdateUserRequest`, `CreateGroupRequest` and `UpdateGroupRequest` (`internal/api/types.go:377`, `:384`, `:417`, `:425`) are referenced by no Go code; the live decode targets are the `auth.API*Request` types. `internal/api/openapi.yaml:6723` and `:6741` still document a `role` property that no decode type has (it was removed in #912); a client that sends `role` has it ignored.
- **Service-config PUT `provider` and `service`** are overwritten by the URL path (`handler_config.go:510-514`), with no comment and no test for a conflicting body value.
- **Marketplace `price_schedule` escape cells.** The tier fields (`term_months`, `price`) are traced to the persisted JSON column and to the client call at `handler_marketplace_attempt.go:199-204`, but not into `cloud-commitments-go`.
- **`acknowledge_unknown_outcome` on outcome `completed`** (E6, E7) is accepted and not stored. The type comments (`handler_ri_exchange_resolve.go:43-45`, `handler_ri_exchange_direct_resolve_request.go:14-17`) state the requirement for `failed` only, so classifying it as deliberately ignored is a judgment call. If that reading is rejected it becomes an ordinary dropped cell. Nothing is released by a `completed` resolve.
- **Approval-token classification (T1, T2).** The token is used as a compare-only predicate in the status-claim UPDATE (`store_postgres.go:1438`, `:1559`) and never persisted. It is labeled "reaches the target" under the FORWARDED rule; "validation or authorization only" is an equally defensible label. No row is dropped either way.
- **`internal/server/handler.go:453` classification.** `ScheduledEvent` (EventBridge, self-invoke or CLI payload) is classed as not an adapter because it is not an API request body, although it copies fields into `ScheduledTaskParams`. `source`, `detail-type` and `detail` are not used after the decode (`handler.go:451-484`).
- **`created_at` and `updated_at` accepted and ignored in the ladder config upsert** (`internal/api/handler_ladder.go:102`, `store_postgres_ladder.go:105,118`), with no comment. The ladder config `provider` is not forwarded to the engine (`internal/server/handler_ladder.go:598`).
- **Silent reset by omission on PUT /api/plans/{id}.** `notification_days_before`, `enabled` and `auto_purchase` are not pointers, so a PUT that omits them writes 0 or false (`types.go:857,864,865`). This is full replacement by design and PATCH is the partial endpoint, but nothing in the API spec says so. If only one of `provider` and `service` is sent, `buildServiceConfig` returns nil and the sent one is ignored (`types.go:954-956`).
- **Legacy `target_offering_id` and `target_count` on the exchange quote and execute bodies** are ignored when `targets` is non-empty (comment at `handler_ri_exchange.go:2221-2223`); the handler forwards both spellings and the library picks one.
- **Azure `billing_scope_id`** is never used for the charge (comments at `handler_ri_exchange.go:512-518`, `:583-595`); on execute the submitted value is recorded in the audit targets JSON (`ri_exchange_audit.go:251`) and no test covers that.
- **`ExecutePurchaseRequest.recommendations[]`**: every element field except `count`, `recommended_count` and `selected` is replaced from the stored row (`purchase_pricing.go:148-151`), documented by one shared comment (`handler_purchases.go:3146-3150`) rather than per field. `capacity_percent` persistence and the `execute_mode` branch have no test.

### Deliberate and documented behavior

| Pair | Field or rule | Source comment | Test |
| --- | --- | --- | --- |
| B5 | `created_at`, `updated_at` cleared | `handler_config.go:502-504` | none |
| B5 | `group_id`, `bound_identities`, `max_step_commitment`, `bound_count` cleared | `handler_config.go:506-507` | `handler_config_test.go:1698` |
| B5 | `ramp_schedule` not overlaid on an existing row | `handler_config.go:349-350` | none |
| B9 | `account_name`, `created_at`, `updated_at` ignored on import | `internal/api/override_transfer.go:14-16` | `handler_override_import_test.go:33` |
| E3, E4 | Azure `billing_scope_id` replaced by the derived scope | `handler_ri_exchange.go:512-518`, `:583-595` | `handler_ri_exchange_azure_test.go:255`, `:440` |
| E9 | cost and identity fields replaced from the stored row | `handler_purchases.go:3146-3150`, `purchase_pricing.go:124-128` | `purchase_pricing_test.go:131-198` |
| R1 | credential fields kept only with a credential store | `handler_registrations.go:111` | `handler_registrations_credentials_test.go:71`, `:129` |
| T1, T2 | token ignored for an authorized session | `handler_purchases.go:1015-1019`, `:1781` | `handler_purchases_test.go:4505` |

## Test coverage

A guard test checks the converter's output only. A round-trip test asserts every field of the pair at the callee boundary (fixture store, SQL or outbound client). "Partial" means named tests assert some fields only.

| Pair | Guard test | Round-trip test | Status |
| --- | --- | --- | --- |
| A1 | `adapter_field_drop_test.go:124` (converter only) | none | guard only |
| A2 | `adapter_field_drop_test.go:124`; `handler_accounts_update_keep_test.go:75`, `:103` (omitted flags) | none | guard only |
| A3 | `adapter_field_drop_test.go:124` | none | guard only |
| A4 | none | none (`handler_accounts_test.go:934` asserts success only) | none |
| A5 | none | none (`discover_org_scope_test.go:173` does not assert the request value) | none |
| R1 | `adapter_field_drop_test.go:128` (`credential_type`, `credential_payload` allowlisted) | `handler_registrations_credentials_test.go:71`, `:129` for the credential fields | guard, partial |
| R2 | `adapter_field_drop_test.go:124` (does not see the later overwrite) | `handler_registrations_approve_test.go:61` (no explicit false) | guard, partial |
| R3 | none | none | none |
| P1 | none | none (`handler_accounts_test.go:1232` asserts success only) | none |
| B1 | `adapter_field_drop_test.go:137` (converter only) | `handler_plans_test.go:298` asserts `SetPlanAccounts` arguments only | guard, partial |
| B2 | `adapter_field_drop_test.go:137`; `description` and `target_accounts` allowlisted, so the update path for `target_accounts` is unguarded | partial: `handler_plans_services_merge_test.go`, `handler_plans_step_cap_test.go:17`, Postgres tests | guard, partial |
| B3 | none | `handler_plans_test.go:1198` | round-trip |
| B4 | none | `handler_plans_concurrency_integration_test.go:135` (Postgres) | round-trip |
| B5 | none | partial: `handler_config_test.go:661`, `:727` | partial |
| B6 | none | `handler_ladder_resolve_test.go:154` | round-trip |
| E1 | none | partial: `handler_ri_exchange_test.go:1892`, `:2297` | partial |
| E2 | none | partial: audit row only, `ri_exchange_audit_test.go:184-187`, `:547-561` | partial |
| E3 | none | partial: Azure call asserted with `mock.Anything`; `handler_ri_exchange_azure_test.go:423` asserts the billing scope | partial |
| E4 | none | partial: audit row only, `ri_exchange_audit_test.go:424-430` | partial |
| E5 | none | none (`global_config_arming_grants_test.go:70` exercises the gate only) | none |
| E6 | none | `handler_ri_exchange_resolve_test.go:75-92` | round-trip |
| E7 | none | `handler_ri_exchange_direct_resolve_test.go:114-137` | round-trip |
| E8 | none | `handler_execution_settle_test.go:20-21`; `handler_execution_settle_integration_test.go:55`, `:88` | round-trip |
| E9 | none | partial: `purchase_pricing_test.go:131-198`; none for `capacity_percent` persistence and `execute_mode` | partial |
| B7 | none | `handler_marketplace_attempt_test.go:441` | round-trip |
| T1 to T4 | none | `handler_purchases_test.go:4505`, `:4547`, `:6427`; token pin tests in `internal/purchase/token_rotation_pin_integration_test.go`; `handler_purchases_approval_details_test.go:117` | round-trip |
| O1 | `handler_override_merge_test.go:193` (request and stored sides) | `handler_override_merge_test.go:59`, `:78`, `:94` | guard and round-trip |
| B8, B9 | none | `handler_override_import_test.go:33`, `:74`; `handler_override_import_integration_test.go:22` (Postgres) | round-trip |
| U1 | none | `internal/server/adapter_mfa_token_test.go:23` (`TestLogin_MFATokenRoundTrip`, named for `mfa_token`); `adapter_test.go:48` | partial |
| U2 | none | `internal/server/adapter_test.go:621` | round-trip |
| U3 to U10 | none | handler-boundary argument tests in `handler_auth_test.go` (`:536`, `:595`, `:835`, `:926`, `:1339`, `:1376`, `:1405`, `:1418`); adapter smoke tests exercise error paths only | partial |

## Appendix: every decode site

Source: `git grep -n -E '(Unmarshal|Decode)\(' 72d90d54b06769710b7c49094365892d9a4d4ab0 -- internal/api internal/server ':!*_test.go'`. 64 lines, 64 rows: 36 adapter, 10 auxiliary, 18 not an adapter. "Auxiliary" marks a second decode of a body already decoded elsewhere (a presence map or a single-document check). Pair ids refer to the area sections.

| Site | Target type | Adapter | Pair(s) fed | Reason |
| --- | --- | --- | --- | --- |
| internal/api/handler_accounts.go:357 | CloudAccountRequest | yes | A1 CloudAccountRequest -> createAccount | fields copied into config.CloudAccount by cloudAccountFromRequest (:548) |
| internal/api/handler_accounts.go:626 | CloudAccountRequest | yes | A2 CloudAccountRequest -> updateAccount | same converter plus keepOmittedAccountFlags (:585); provider is checked, not written |
| internal/api/handler_accounts.go:802 | CredentialsRequest | yes | A4 CredentialsRequest -> saveAccountCredentials | credential_type and the marshalled payload leave as positional arguments of credStore.SaveCredential (:839) |
| internal/api/handler_accounts.go:1193 | AccountServiceOverrideRequest | yes | O1 AccountServiceOverrideRequest -> saveAccountServiceOverride | fields overlaid onto config.AccountServiceOverride by buildServiceOverride (:1245); the key-presence decode at handler_config.go:389 is an auxiliary of this pair |
| internal/api/handler_accounts.go:1461 | anonymous struct {AccountIDs []string `json:"account_ids"`} | yes | P1 anonymous account_ids body -> setPlanAccounts | account_ids leaves as a positional argument of SetPlanAccountsGated (:1494) |
| internal/api/handler_accounts.go:1661 | DiscoverOrgRequest | yes | A5 DiscoverOrgRequest -> discoverOrgAccounts | account_id is passed positionally to requireAccountAccess (:1671) as a lookup key; the stored root, not the request value, drives the writes |
| internal/api/handler_apikeys.go:63 | auth.APICreateAPIKeyRequest | no | none (direct decode into the auth service's own wire type, passed whole to CreateAPIKeyAPI at :75; service-side conversion is internal/auth, #923) | decoded straight into the type the callee takes; the handler only decodes the password field in place |
| internal/api/handler_auth.go:30 | LoginRequest | yes | U1 LoginRequest -> login | copied field by field into auth.LoginRequest by authServiceAdapter.Login (internal/server/app.go:1179-1184) |
| internal/api/handler_auth.go:374 | SetupAdminRequest | yes | U2 SetupAdminRequest -> setupAdmin | copied into auth.SetupAdminRequest by authServiceAdapter.SetupAdmin (app.go:1218-1221) |
| internal/api/handler_auth.go:414 | PasswordResetRequest | yes | U3 PasswordResetRequest -> forgotPassword | email leaves as positional argument 2 of RequestPasswordReset (:435) |
| internal/api/handler_auth.go:505 | PasswordResetConfirm | yes | U4 PasswordResetConfirm -> resetPassword | copied into auth.PasswordResetConfirm by authServiceAdapter.ConfirmPasswordReset (app.go:1248-1251) |
| internal/api/handler_auth.go:545 | ProfileUpdateRequest | yes | U5 ProfileUpdateRequest -> updateProfile | email, current_password, new_password leave as positional arguments 3-5 of UpdateUserProfile (:563; app.go:1276) |
| internal/api/handler_auth.go:649 | ChangePasswordRequest | yes | U6 ChangePasswordRequest -> changePassword | current_password and new_password leave as positional arguments 3-4 of ChangePasswordAPI (:659; app.go:1297) |
| internal/api/handler_auth.go:719 | MFASetupRequest | yes | U7 MFASetupRequest -> mfaSetup | password leaves as positional argument 3 of MFASetupAPI (:727) |
| internal/api/handler_auth.go:743 | MFAEnableRequest | yes | U8 MFAEnableRequest -> mfaEnable | code leaves as positional argument 3 of MFAEnableAPI (:747) |
| internal/api/handler_auth.go:764 | MFADisableRequest | yes | U9 MFADisableRequest -> mfaDisable | password and code leave as positional arguments 3-4 of MFADisableAPI (:772) |
| internal/api/handler_auth.go:789 | MFARegenerateRequest | yes | U10 MFARegenerateRequest -> mfaRegenerateRecoveryCodes | code leaves as positional argument 3 of MFARegenerateRecoveryCodesAPI (:793) |
| internal/api/handler_config.go:116 | config.GlobalConfig (the stored row, inside UpdateGlobalConfigAtomic) | no | none | Direct decode into the store struct under an advisory-locked read-modify-write; absent keys are preserved by design (comment handler_config.go:85-95). Not a copy into another struct. |
| internal/api/handler_config.go:144 | map[string]json.RawMessage (presence map) | auxiliary | none (auxiliary of a direct decode at internal/api/handler_config.go:116) | globalConfigWriteFields: presence map used for grace_period_days replace and mode and provider validation only. |
| internal/api/handler_config.go:389 | map[string]json.RawMessage (presence map) | auxiliary | B5 | presentKeys: tells mergeServiceConfig which overlay keys the service-config PUT body carried. |
| internal/api/handler_config.go:499 | config.ServiceConfig (request, then overlaid onto the stored row) | yes | B5 | Decoded into the store struct, but mergeServiceConfig copies an allowlist of fields onto the stored row (handler_config.go:351-357,369-382), so fields outside it never reach the store for an existing row; body provider and service are overwritten by the path. |
| internal/api/handler_execution_settle.go:142 | executionSettleRequest (handler_execution_settle.go:26) | yes | E8: executionSettleRequest via settleExecution | Body is converted into config.ExecutionSettleRequest (:70-72). Strict decoder (:141). |
| internal/api/handler_execution_settle.go:145 | `new(any)` (single-document EOF check) | auxiliary | E8 (same body as :142) | Rejects a second JSON document after the body; carries no field. |
| internal/api/handler_groups.go:50 | auth.APICreateGroupRequest | no | none (direct decode into the auth service's own wire type, passed whole to CreateGroupAPI at :60; service-side conversion is internal/auth, #923) | decoded straight into the callee's input type; the handler only rewrites allowed_accounts in place (bindGroupAccounts :55) |
| internal/api/handler_groups.go:123 | auth.APIUpdateGroupRequest | no | none (direct decode into the auth service's own wire type, passed whole to UpdateGroupAPI at :132; service-side conversion is internal/auth, #923) | decoded straight into the callee's input type; the handler only rewrites allowed_accounts in place (:127) |
| internal/api/handler_ladder.go:102 | config.LadderConfigDB (store struct) | no | none | Direct decode (DisallowUnknownFields) straight into the store struct passed to UpsertLadderConfig; no per-field copy. |
| internal/api/handler_ladder.go:117 | map[string]json.RawMessage (presence map) | auxiliary | none (auxiliary of a direct decode at internal/api/handler_ladder.go:102) | Presence map so defaults apply only to omitted numeric keys (applyLadderConfigNumericDefaults). |
| internal/api/handler_ladder_amend.go:86 | config.LadderAmendment (store struct) | no | none | Decoded into the store-layer amendment value and passed whole to AmendLadderTranche; all three fields used (store_postgres_ladder_amend.go:94,97,105-107). |
| internal/api/handler_ladder_amend.go:89 | any (EOF probe, value discarded) | auxiliary | none (auxiliary of a direct decode at internal/api/handler_ladder_amend.go:86) | Rejects a second JSON document after the amendment. |
| internal/api/handler_ladder_resolve.go:155 | ladderResolveRequest | yes | B6 | Body is rebuilt into config.LadderResolveFailed and a status_reason string (handler_ladder_resolve.go:127-130,195-225). |
| internal/api/handler_ladder_resolve.go:158 | any (EOF probe, value discarded) | auxiliary | B6 | Rejects a second JSON document after the resolve body. |
| internal/api/handler_marketplace.go:227 | MarketplaceListRequest | yes | B7 | price_schedule tiers are converted (toAWSPriceSchedule) into the EC2 listing request and persisted as JSON. |
| internal/api/handler_marketplace_attempt.go:133 | []MarketplacePriceTier (stored value) | no | none | Decodes the persisted listing_price_schedule of a resumed attempt read from the store, not an API request; it replaces the request's price_schedule on resume (documented at handler_marketplace_attempt.go:93-94). |
| internal/api/handler_override_import.go:130 | OverrideImportRequest | yes | B8, B9 | Request fields feed the report and audit event (B8); its rows (JSON `overrides` or decoded `csv`) are copied into config.AccountServiceOverride (B9). |
| internal/api/handler_plans.go:168 | PlanRequest | yes | B1, B2 | decodePlanRequest serves createPlan (:184) and updatePlan (:319); toPurchasePlan copies the fields into config.PurchasePlan. |
| internal/api/handler_plans.go:327 | anonymous struct {max_step_commitment, expected_updated_at} | auxiliary | B2 | Second decode of the update body for cap presence and the optimistic-lock token; its fields are rows of the B2 table. |
| internal/api/handler_plans.go:810 | CreatePlannedPurchasesRequest | yes | B4 | start_date and count leave as positional arguments to createPlannedPurchasesTx. |
| internal/api/handler_plans.go:969 | PatchPlanRequest | yes | B3 | applyPatchFields copies the four optional fields onto the stored config.PurchasePlan. |
| internal/api/handler_purchases.go:3233 | ExecutePurchaseRequest (handler_purchases.go:3145) | yes | E9: ExecutePurchaseRequest via executePurchase | Recommendations are re-priced from the stored set (priceFromStored, purchase_pricing.go:131) and copied with capacity_percent into config.PurchaseExecution by newPendingExecution (handler_purchases.go:3771). Non-strict decoder. |
| internal/api/handler_purchases_revoke.go:296 | revokeConfirmBody (handler_purchases_revoke.go:74) | no | none (validation only; | The decoded body is passed whole to dispatchProviderRevoke (:301) and used only to compare the user's confirmed quote with Azure's fresh quote (checkConfirmedRefund :1016); no field is copied into another struct or call argument. Decoded only on the completed-purchase path; the scheduled-execution path never reads the body. Non-strict decoder. |
| internal/api/handler_registrations.go:91 | RegistrationRequest | yes | R1 RegistrationRequest -> submitRegistration | copied into config.AccountRegistration by registrationFromRequest (:60); credential fields attached in submitRegistration (:106-114) |
| internal/api/handler_registrations.go:352 | CloudAccountRequest | yes | R2 CloudAccountRequest -> approveRegistration | approval body converted by cloudAccountFromRequest (:294); enabled is overwritten at :303 |
| internal/api/handler_registrations.go:402 | RejectRequest | yes | R3 RejectRequest -> rejectRegistration | reason copied into config.AccountRegistration.RejectionReason (:408) |
| internal/api/handler_registrations_credentials.go:37 | map[string]interface{} (the credential_payload string's JSON object) | auxiliary | R1 RegistrationRequest -> submitRegistration | validation-and-canonicalization decode of the credential_payload field of the body decoded at handler_registrations.go:91; the same function also re-checks the stored payload at approval (:83) |
| internal/api/handler_ri_exchange.go:981 | AzureCompatibleOfferingsRequestBody (handler_ri_exchange.go:524) | yes | E3: AzureCompatibleOfferingsRequestBody via getAzureCompatibleOfferings | Body is copied into provider-layer reservation and target structs (toAzureExchangeSources :677, toAzureExchangeTargets :696) and handed to the Azure client's CalculateExchange (:1009). Non-strict decoder: unknown keys are ignored. |
| internal/api/handler_ri_exchange.go:1222 | AzureExecuteExchangeRequestBody (handler_ri_exchange.go:544) | yes | E4: AzureExecuteExchangeRequestBody via executeAzureExchange | Same copies as E3 plus the audit row (azureDirectExchangeAudit, ri_exchange_audit.go:239), the idempotency key (ri_exchange_idempotency.go:103) and the spend-cap guardrails. Decoded inside parseAzureExecuteRequest (:1220). Non-strict decoder. |
| internal/api/handler_ri_exchange.go:1956 | ExchangeQuoteRequestBody (handler_ri_exchange.go:2224) | yes | E1: ExchangeQuoteRequestBody via getExchangeQuote | Fields are copied into exchange.ExchangeQuoteRequest (:1972-1978) with targets converted by toExchangeTargets (:2251). Non-strict decoder. |
| internal/api/handler_ri_exchange.go:2029 | ExchangeExecuteRequestBody (handler_ri_exchange.go:2238) | yes | E2: ExchangeExecuteRequestBody via executeExchange | Fields are copied into exchange.ExchangeExecuteRequest (:2071-2078), the audit row (awsDirectExchangeAudit, ri_exchange_audit.go:184, which folds the legacy target_offering_id/target_count into a one-element targets list) and the idempotency key (ri_exchange_idempotency.go:137, same fold). Non-strict decoder. |
| internal/api/handler_ri_exchange.go:2305 | RIExchangeConfigUpdateRequest (handler_ri_exchange.go:3103) | yes | E5: RIExchangeConfigUpdateRequest via updateRIExchangeConfig | Six fields are copied one by one onto config.GlobalConfig inside UpdateGlobalConfigAtomic's closure (:2335-2340). Non-strict decoder. |
| internal/api/handler_ri_exchange_direct_resolve_request.go:41 | riExchangeDirectResolveRequest (handler_ri_exchange_direct_resolve_request.go:18) | yes | E7: riExchangeDirectResolveRequest via resolveRIExchangeDirectExecution (build step buildRIExchangeDirectResolve, handler_ri_exchange_direct_resolve.go:195) | Body is converted into config.RIExchangeDirectResolve (handler_ri_exchange_direct_resolve.go:204-206) with reason, acknowledgement, region and time folded into a note by body.note (:98). Strict decoder (:40). |
| internal/api/handler_ri_exchange_direct_resolve_request.go:44 | `new(any)` (single-document EOF check) | auxiliary | E7 (same body as :41) | Rejects a second JSON document after the body; carries no field. |
| internal/api/handler_ri_exchange_resolve.go:208 | riExchangeResolveRequest (handler_ri_exchange_resolve.go:46) | yes | E6: riExchangeResolveRequest via resolveRIExchange | Body is converted into config.RIExchangeResolve (:96-99) with the reason, acknowledgement and cloud check folded into a note and a RIExchangeCloudCheck by body.note (:271) and body.cloudCheck (:256). Strict decoder (DisallowUnknownFields at :207). |
| internal/api/handler_ri_exchange_resolve.go:211 | `new(any)` (single-document EOF check) | auxiliary | E6 (same body as :208) | Rejects a second JSON document after the body; carries no field. |
| internal/api/handler_users.go:48 | auth.APICreateUserRequest | no | none (direct decode into the auth service's own wire type, passed whole to CreateUserAPI at :70; service-side conversion is internal/auth, #923) | decoded straight into the callee's input type; the handler only decodes the password field in place (:61-65) |
| internal/api/handler_users.go:168 | auth.APIUpdateUserRequest | no | none (direct decode into the auth service's own wire type, passed whole to UpdateUserAPI at :176; service-side conversion is internal/auth, #923) | decoded straight into the callee's input type; the handler does not touch any field |
| internal/api/ri_utilization_cache.go:202 | `[]riUtilization` (alias of recommendations.RIUtilization) | no | none | Reads the cache payload this process wrote itself (`storePayload` json.Marshal, ri_utilization_cache.go:174); not an API request body. |
| internal/api/router.go:608 | anonymous `struct{ Token string }` (router.go:605-607) | yes | T1 approvePurchase, T2 cancelPurchase, T3 revokeViaEmailToken, T4 getApprovalDetails | POST body token returned by `resolveApprovalToken` and passed as a positional argument to four handlers. |
| internal/server/handler.go:453 | `ScheduledEvent` | no | none | EventBridge, self-invoke or CLI scheduled event, not an API request body; copied to `ScheduledTaskParams`. |
| internal/server/handler_ladder.go:592 | pkgladder.RampSchedule (engine type) | no | none | Parses the stored ramp_schedule column of a LadderConfigDB row inside ladderConfigToEngine; a store-to-engine hop, not an API request decode (see the ladder section). |
| internal/server/lambda.go:60 | anonymous `httpEvent` struct | no | none | Event-type sniffing only (reads the HTTP method keys); nothing is forwarded. |
| internal/server/lambda.go:72 | anonymous `scheduledEvent` struct | no | none | Event-type sniffing only (reads source, detail-type, action); nothing is forwarded. |
| internal/server/lambda.go:87 | `events.LambdaFunctionURLRequest` | no | none | AWS envelope decoded straight into the type `API.HandleRequest` takes (lambda.go:136); handler body decodes belong to the per-route pairs. |
| internal/server/scheduledauth/kidguard.go:100 | `jose.JSONWebKeySet` | no | none | Identity-provider JWKS response parse inside a RoundTripper; not an API request body. |
| internal/server/scheduledauth/validator.go:319 | anonymous `{Keys []json.RawMessage}` | no | none | Warmup probe checks that the JWKS response has a `keys` array; validation of a provider response only. |

### Pairs with no decode site of their own

| Pair | Where the struct comes from |
| --- | --- |
| A3 CloudAccountRequest -> createSelfAccount | built in code by `buildSelfAccountRequest` (`internal/api/handler_accounts.go:327`, called `:284`) from the host source identity; no request body is read; it goes through `cloudAccountFromRequest` (`:290`) into `CreateCloudAccount` (`:296`), the same converter as A1, A2 and R2 |
| B9 (CSV form) | `decodeOverridesCSV` (`internal/api/override_transfer_csv.go:117-142`) parses the `csv` string of the B8 body into `OverrideTransferRow` values; no `Unmarshal` or `Decode` call, so no grep line |
| B2 (anonymous cap struct) | decoded at `internal/api/handler_plans.go:327`, listed in the appendix as an auxiliary decode of B2 |
| ladder timeline scope | `ladderTimelineRequest` (`internal/api/handler_ladder_timeline.go:16-21`) is filled from query-string parameters; not an API body and not a pair |

## How to keep it true

- **`requireFieldsReach`** (`internal/api/adapter_field_drop_test.go:96-122`) is the reflection helper behind the field-drop guards for three converters: `CloudAccountRequest` (`:124`), `RegistrationRequest` (`:128`) and `PlanRequest` (`:137-147`). It fails CI when a field is added to the request struct and the converter does not map it, unless the field is on the test's allowlist.
- **`TestAccountServiceOverrideRequest_EveryFieldIsMerged`** (`internal/api/handler_override_merge_test.go:193`) guards the override request on both the request and the stored side, with a per-key table at `:59`.
- **#923's guards** cover the `internal/auth` conversions behind the auth adapters. Nothing in this audit replaces them.
- **`TestLogin_MFATokenRoundTrip`** (`internal/server/adapter_mfa_token_test.go:23`) is the only per-field guard on the `internal/server` auth adapters, and it is single-field: it asserts `mfa_token`. The copy in `authServiceAdapter.Login` (`internal/server/app.go:1178-1184`) has no reflection guard, so a field added to `api.LoginRequest` is not caught automatically.

Limits to keep in mind when relying on these:

- A converter guard covers the converter's output, not the store write. The approval path passed its guard while `handler_registrations.go:303` overwrote `enabled` after the converter ran.
- `PlanRequest`'s allowlist (`description` at `adapter_field_drop_test.go:145`, `target_accounts` at `:146`) means the update path for target accounts and description (#1776) is unguarded: the entry proves only that the converter does not map them, not that the update path ignores them on purpose, and no source comment in `handler_plans.go` says so.
- The guards do not see merge steps (`mergeUpdatedPlan`), a second decode of the same body, `SetPlanAccounts` or any store write.
- The other pairs in this audit are covered by reading only, until the round-trip tests in the follow-up issue land.

To refresh the audit after a change: re-run the decode-site grep above and diff its line count against the appendix (64 at the audited commit); then re-run `git diff 72d90d54b06769710b7c49094365892d9a4d4ab0..origin/main -- internal/api internal/server ':!*_test.go'` and re-derive every pair whose converter, request struct or target struct the diff touches. A copy line removed from an existing converter changes no decode site, so the grep alone would not show it.

## Differences from the old issue comment

The comment on #19 of 2026-11-05 predates #1788, #1797 and later changes. Where it disagrees with the audited commit:

- "No per-adapter round-trip test for CloudAccountRequest, RegistrationRequest, PlanRequest or AccountServiceOverrideRequest": stale. Guards exist (`adapter_field_drop_test.go:124`, `:128`, `:137`; `handler_override_merge_test.go:193`). They cover converter output, not the store write.
- #1775 (override save clears the other fields): fixed. `saveAccountServiceOverride` overlays only the keys present in the body inside a locked read-merge-write transaction (`handler_accounts.go:1198-1221`, `:1245-1299`).
- `RIExchangeConfigUpdateRequest` listed as clean: one value is not clean. `utilization_threshold` 0 was stored as 95 (E5, fixed in #1906).
- `approveRegistration`'s `enabled` was listed under #1776 as accepted but ignored: confirmed at `handler_registrations.go:303` and fixed in #1905.
- Registrations listed as clean: true for the mapper. `credential_type` and `credential_payload` are not in `registrationFromRequest` and are attached in `submitRegistration` only when a credential store is configured (`handler_registrations.go:106-114`).
- The scope covered `internal/api` only. The `internal/server` auth adapters (`authServiceAdapter` `Login`, `SetupAdmin`, `ConfirmPasswordReset`), the profile and MFA bodies, and the approval-token body (`router.go:605-608`, four routes) were not covered and are not covered by #923; none drops a field.
- The service-config PUT body `provider` and `service` are overwritten by the URL path (`handler_config.go:510-514`); #1776 does not list them.
- The legacy `target_offering_id` and `target_count` are not folded into `targets` by the handler on the library path; they are forwarded and the library picks one. The fold happens only in the audit row (`ri_exchange_audit.go:185-188`) and the idempotency key (`ri_exchange_idempotency.go:148-156`).
- Azure `billing_scope_id` is never used for the charge; on execute the submitted value is still recorded in the audit targets JSON (`ri_exchange_audit.go:251`).
- "Deliberate: ExecutePurchaseRequest client costs" is narrower than the code: every `recommendations[]` field except `count`, `recommended_count` and `selected` is replaced from the stored row.
- The revoke confirm body is a validation input, not an adapter, and is decoded only on the completed-purchase path (`handler_purchases_revoke.go:294-299`).
- Dead request types and the stale OpenAPI `role` property were not mentioned (see Findings).
- Line numbers: `requireFieldsReach` is `adapter_field_drop_test.go:96-122` and the three guard tests are `:124`, `:128` and `:137-147`. The plan update body is decoded by `decodePlanRequest` (decode site `handler_plans.go:168`, call `:319`) and again into an anonymous struct at `:327`.
