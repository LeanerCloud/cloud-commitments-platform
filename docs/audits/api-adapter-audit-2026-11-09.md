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
