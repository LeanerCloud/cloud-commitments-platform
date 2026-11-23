package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"text/template"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	cudlyiac "github.com/LeanerCloud/cloud-commitments-platform/iac"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/iacfiles"
	"github.com/aws/aws-lambda-go/events"
)

// federationIaCData holds the template variables used when rendering IaC templates.
type federationIaCData struct {
	AccountName       string
	AccountExternalID string
	AccountSlug       string
	Source            string
	SourceAccountID   string // AWS account ID where CUDly runs (resolved via STS)
	// AWS WIF / cross-account
	OIDCIssuerURL string
	OIDCAudience  string
	// OIDCSubjectClaim restricts the AWS trust policy to a single workload
	// subject. Deliberately left empty by every generic-bundle builder below —
	// CUDly's server has no generic way to know the calling workload's real
	// subject claim (it is not derivable from target/source alone the way
	// OIDCIssuerURL/OIDCAudience are). Every AWS-WIF template still emits this
	// field as a required, uncommented value so the operator must fill it in
	// before the bundle deploys/applies/runs, rather than the bundle silently
	// working with no :sub condition (see #1543, #1602, #1640).
	OIDCSubjectClaim string
	// Azure-specific
	SubscriptionID string
	TenantID       string
	// GCP-specific
	ProjectID           string
	ServiceAccountEmail string
	OIDCIssuerURI       string
	CUDlyAPIURL         string
	// ContactEmail is the email of the authenticated user who downloaded the bundle.
	// Pre-filled from Session.Email — no manual entry required.
	ContactEmail string
}

// FederationIaCResponse is returned by the /api/federation/iac endpoint.
type FederationIaCResponse struct {
	Filename        string `json:"filename"`
	Content         string `json:"content"`
	ContentType     string `json:"content_type"`
	ContentEncoding string `json:"content_encoding,omitempty"` // "base64" for binary (zip)
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	return strings.Trim(slugRE.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

func awsOIDCIssuer(source, tenantID string) string {
	switch source {
	case "azure":
		if tenantID != "" {
			return "https://login.microsoftonline.com/" + tenantID + "/v2.0"
		}
		return "https://login.microsoftonline.com/<AZURE_TENANT_ID>/v2.0"
	case "gcp":
		return "https://accounts.google.com"
	default:
		return ""
	}
}

func awsOIDCAudience(source string) string {
	if source == "azure" {
		return "api://AzureADTokenExchange"
	}
	return "sts.amazonaws.com"
}

func gcpOIDCIssuerURI(source, tenantID string) string {
	switch source {
	case "azure":
		if tenantID != "" {
			return "https://login.microsoftonline.com/" + tenantID + "/v2.0"
		}
		return "https://login.microsoftonline.com/<AZURE_TENANT_ID>/v2.0"
	default:
		return ""
	}
}

// renderTemplate renders a named template from the embedded iacfiles.Templates FS.
// It refuses data holding a line break or other control character; see
// checkRenderDataSingleLine.
func renderTemplate(tmplPath string, data federationIaCData) (string, error) {
	if err := checkRenderDataSingleLine(data); err != nil {
		return "", fmt.Errorf("render template %s: %w", tmplPath, err)
	}
	return executeTemplate(tmplPath, data)
}

// executeTemplate parses and executes a template with no checks on data.
// Only renderTemplate calls it outside tests.
func executeTemplate(tmplPath string, data federationIaCData) (string, error) {
	tmplBytes, err := iacfiles.Templates.ReadFile(tmplPath)
	if err != nil {
		return "", fmt.Errorf("read template %s: %w", tmplPath, err)
	}
	tmpl, err := template.New("iac").Parse(string(tmplBytes))
	if err != nil {
		return "", fmt.Errorf("parse template %s: %w", tmplPath, err)
	}
	var buf bytes.Buffer
	if err = tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render template %s: %w", tmplPath, err)
	}
	return buf.String(), nil
}

// checkRenderDataSingleLine fails closed when any string field of data holds
// a control character other than a horizontal tab. The shell and tfvars
// templates print several fields on "#" comment lines, and shellEscape and
// hclEscape escape quoting metacharacters but not line breaks, so a CR or LF
// there would end the comment and run the rest of the value as shell or HCL
// (#970). The generic bundles fill no stored account fields today, and the
// account write paths reject control characters, so this guard only trips on
// data that bypassed them (a record stored before that validation, a future
// caller that fills account fields, or a bad config value); an error is safer
// than a script that runs something unintended.
// A tab is allowed because it cannot end a line, and the session email can
// legitimately hold one in a quoted local part. It walks the struct by
// reflection so a new field cannot be missed.
func checkRenderDataSingleLine(data federationIaCData) error {
	v := reflect.ValueOf(data)
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() != reflect.String {
			continue
		}
		if at := strings.IndexFunc(f.String(), isRenderControlChar); at >= 0 {
			r, _ := utf8.DecodeRuneInString(f.String()[at:])
			return fmt.Errorf("field %s contains control character %U", v.Type().Field(i).Name, r)
		}
	}
	return nil
}

// isRenderControlChar reports whether r is a control character that
// checkRenderDataSingleLine refuses: every C0 and C1 control and DEL except
// the horizontal tab.
func isRenderControlChar(r rune) bool {
	return r != '\t' && unicode.IsControl(r)
}

// getFederationIaC handles GET /api/federation/iac
// Query params: target, source, format — all required.
//
// Generates generic IaC templates for self-registration. Target account owners
// fill in their own values via -var flags when running terraform apply.
//
// The rendered template embeds the CUDly host's AWS account ID (resolved via
// STS GetCallerIdentity) so target-account deployments can trust the right
// source. That identity must not leak to unauthenticated callers — the
// endpoint requires view:accounts, matching the audience that legitimately
// consumes these templates (admins onboarding cross-account federations).
//
//   - format="cli"      → self-contained shell script (single file)
//   - format="cfn"      → zip with CFN template + params + deploy script (aws target only)
//   - format="bicep"    → zip with Bicep template + params + deploy script (azure target only)
//   - format="arm"      → zip with ARM JSON template + params + deploy script (azure target only)
//   - format="bundle"   → zip with Terraform module + pre-filled tfvars (and CFN fallback on aws)
func (h *Handler) getFederationIaC(ctx context.Context, req *events.LambdaFunctionURLRequest) (*FederationIaCResponse, error) {
	session, err := h.requirePermission(ctx, req, "view", "accounts")
	if err != nil {
		return nil, err
	}

	target, source, format, err := federationIaCParams(req.QueryStringParameters)
	if err != nil {
		return nil, err
	}

	// Reject impossible target/source combinations early — before bundle
	// construction — so the caller gets a clear 400 instead of a downloadable
	// bundle that fails at terraform apply with a cryptic IAM error. See #42.
	if err := validateFederationTargetSource(target, source); err != nil {
		return nil, err
	}

	apiURL := deriveFederationAPIURL(h.dashboardURL, req.RequestContext.DomainName)
	data := buildGenericIaCData(target, source, apiURL)
	if err := h.populateSourceAccountID(ctx, source, &data); err != nil {
		return nil, err
	}
	if err := h.validateSourceIdentity(ctx); err != nil {
		return nil, err
	}
	// ContactEmail is always the email of the authenticated user who requested
	// the bundle — the route is Auth: AuthUser so session.Email is always set
	// for normal traffic. No env-var override, no admin-curated fallback: the
	// person who downloads the bundle IS the contact.
	data.ContactEmail = session.Email

	if formatNeedsZip(format) {
		return h.buildZipResponse(data, target, source, format, "target")
	}
	return h.renderSingleFile(data, target, source, format)
}

// deriveFederationAPIURL returns the configured dashboard URL, or derives it
// from the Lambda Function URL domain name (trusted, not the Host header).
func deriveFederationAPIURL(configured, lambdaDomain string) string {
	if configured != "" {
		return configured
	}
	if lambdaDomain != "" {
		return "https://" + lambdaDomain
	}
	return ""
}

// populateSourceAccountID fills data.SourceAccountID when CUDly runs on AWS,
// and returns an error if the cross-account bundle would embed an empty account ID.
// WIF targets (source=gcp|azure) don't use SourceAccountID — their trust is based
// on the OIDC issuer, not CUDly's AWS account number, so no error is returned for them.
func (h *Handler) populateSourceAccountID(ctx context.Context, source string, data *federationIaCData) error {
	if sourceCloud() != "aws" {
		return nil
	}
	data.SourceAccountID = h.resolveSourceAccountID(ctx)
	if source == "aws" && data.SourceAccountID == "" {
		// resolveAWSAccountID returns "" on AWS SDK config load failure or STS
		// GetCallerIdentity failure. Check Lambda logs for the preceding
		// "Failed to resolve source account ID via STS" warning, and verify
		// the execution role has sts:GetCallerIdentity and AWS_REGION is set.
		return fmt.Errorf("federation iac: CUDly failed to resolve its own AWS account ID; " +
			"check that the execution role has sts:GetCallerIdentity and AWS_REGION is set")
	}
	return nil
}

// validateSourceIdentity asserts that CUDly's own source-cloud identity is
// fully populated before rendering an IaC bundle. The Azure and GCP paths
// read identity from environment variables (AZURE_SUBSCRIPTION_ID,
// AZURE_TENANT_ID, GCP_PROJECT_ID) — when any of those are unset on the
// Lambda/Function App, empty strings flow into the rendered tfvars and the
// customer's `terraform apply` later fails with a blank client_id or missing
// project. Fail loud here with a 500-class error naming the missing env var
// so the operator knows exactly what to fix. See #41.
//
// AWS source-cloud is intentionally NOT covered here — populateSourceAccountID
// already enforces the equivalent fail-loud guard for AWS deployments via STS
// GetCallerIdentity, with a more specific error message naming the IAM
// permission the execution role needs.
func (h *Handler) validateSourceIdentity(ctx context.Context) error {
	cloud := sourceCloud()
	if cloud != "azure" && cloud != "gcp" {
		return nil
	}
	id := h.resolveSourceIdentity(ctx)
	switch cloud {
	case "azure":
		if id.SubscriptionID == "" {
			return fmt.Errorf("federation iac: AZURE_SUBSCRIPTION_ID is not set; " +
				"check the Lambda/Function App environment variables")
		}
		if id.TenantID == "" {
			return fmt.Errorf("federation iac: AZURE_TENANT_ID is not set; " +
				"check the Lambda/Function App environment variables")
		}
	case "gcp":
		if id.ProjectID == "" {
			return fmt.Errorf("federation iac: GCP_PROJECT_ID is not set; " +
				"check the Cloud Run / Cloud Functions environment variables")
		}
	}
	return nil
}

// validateFederationTargetSource rejects impossible target/source combinations
// for the current CUDly deployment. It runs before bundle construction so the
// caller gets a clear 400 instead of a downloadable bundle that fails at
// `terraform apply` with a cryptic provider error. See #42.
//
// The rule: a self-source bundle (target == source) requires CUDly to be
// deployed on the matching cloud. When target != source the bundle uses WIF
// and carries no source-cloud identity, so any deployment can render it.
//
// Covered combinations (see also #140):
//   - target=aws  + source=aws  on non-AWS  → 400 (aws-cross-account)
//   - target=azure + source=azure on non-Azure → 400 (azure-self-source)
//   - target=gcp  + source=gcp  on non-GCP  → 400 (gcp-self-source)
func validateFederationTargetSource(target, source string) error {
	if target == source && sourceCloud() != target {
		return NewClientError(400, fmt.Sprintf(
			"target=%s-self-source requires CUDly to be deployed on %s; "+
				"this deployment is on %s", target, strings.ToUpper(target), sourceCloud()))
	}
	return nil
}

// renderSingleFile renders a single-file IaC template (currently only "cli" is supported).
func (h *Handler) renderSingleFile(data federationIaCData, target, source, format string) (*FederationIaCResponse, error) {
	tmplPath, filename, contentType, err := singleFileSpec(target, source, format, "target")
	if err != nil {
		return nil, err
	}
	renderData := data
	if format == "cli" {
		renderData = shellEscapeData(data)
	}
	content, err := renderTemplate(tmplPath, renderData)
	if err != nil {
		return nil, fmt.Errorf("federation iac: %w", err)
	}
	return &FederationIaCResponse{Filename: filename, Content: content, ContentType: contentType}, nil
}

// shellEscapeData returns a copy of data with all fields interpolated into CLI
// shell templates escaped for safe use inside double-quoted bash strings.
func shellEscapeData(data federationIaCData) federationIaCData {
	d := data
	d.AccountName = shellEscape(data.AccountName)
	d.AccountExternalID = shellEscape(data.AccountExternalID)
	d.AccountSlug = shellEscape(data.AccountSlug)
	d.Source = shellEscape(data.Source)
	d.OIDCIssuerURL = shellEscape(data.OIDCIssuerURL)
	d.OIDCAudience = shellEscape(data.OIDCAudience)
	d.OIDCSubjectClaim = shellEscape(data.OIDCSubjectClaim)
	d.SubscriptionID = shellEscape(data.SubscriptionID)
	d.TenantID = shellEscape(data.TenantID)
	d.ProjectID = shellEscape(data.ProjectID)
	d.ServiceAccountEmail = shellEscape(data.ServiceAccountEmail)
	d.OIDCIssuerURI = shellEscape(data.OIDCIssuerURI)
	d.CUDlyAPIURL = shellEscape(data.CUDlyAPIURL)
	d.SourceAccountID = shellEscape(data.SourceAccountID)
	d.ContactEmail = shellEscape(data.ContactEmail)
	return d
}

// formatNeedsZip returns true for IaC formats that ship as multi-file zip archives.
func formatNeedsZip(format string) bool {
	switch format {
	case "bundle", "cfn", "bicep", "arm":
		return true
	}
	return false
}

// buildZipResponse is the single encoder for zip-format IaC downloads. It dispatches
// to the appropriate builder, which returns raw bytes + filename, then base64-wraps
// the result into a FederationIaCResponse.
func (h *Handler) buildZipResponse(data federationIaCData, target, source, format, slug string) (*FederationIaCResponse, error) {
	var (
		zipBytes []byte
		filename string
		err      error
	)
	switch format {
	case "bundle":
		zipBytes, filename, err = buildFederationBundle(data, target, source, slug)
	case "cfn":
		zipBytes, filename, err = buildCFNZip(data, target, source, slug)
	case "bicep", "arm":
		zipBytes, filename, err = buildAzureTemplateZip(format, data, target, slug)
	default:
		return nil, NewClientError(400, "unsupported zip format: "+format)
	}
	if err != nil {
		return nil, err
	}
	return &FederationIaCResponse{
		Filename:        filename,
		Content:         base64.StdEncoding.EncodeToString(zipBytes),
		ContentType:     "application/zip",
		ContentEncoding: "base64",
	}, nil
}

// buildGenericIaCData builds template data with source-specific OIDC values.
// Account-specific fields are left empty — the user provides them at apply time.
func buildGenericIaCData(target, source, dashboardURL string) federationIaCData {
	data := federationIaCData{
		AccountSlug: "target",
		Source:      source,
		CUDlyAPIURL: dashboardURL,
	}
	switch target {
	case "aws":
		data.OIDCIssuerURL = awsOIDCIssuer(source, "")
		data.OIDCAudience = awsOIDCAudience(source)
	case "gcp":
		data.OIDCIssuerURI = gcpOIDCIssuerURI(source, "")
	}
	return data
}

// validFederationSources is the allowlist of valid source cloud providers.
var validFederationSources = map[string]bool{"aws": true, "azure": true, "gcp": true}

// validFederationFormats is the allowlist of supported IaC format codes. The
// legacy empty/"tfvars" single-file format was removed — the full bundle
// supersedes it.
var validFederationFormats = map[string]bool{
	"cli":    true,
	"bundle": true,
	"cfn":    true,
	"bicep":  true,
	"arm":    true,
}

// federationIaCParams validates and extracts query parameters from the request.
func federationIaCParams(q map[string]string) (target, source, format string, err error) {
	target, source, format = q["target"], q["source"], q["format"]
	if target == "" || source == "" || format == "" {
		return "", "", "", NewClientError(400, "target, source, and format query parameters are required")
	}
	if !validFederationSources[source] {
		return "", "", "", NewClientError(400, "source must be aws, azure, or gcp")
	}
	if !validFederationTargets[target] {
		return "", "", "", NewClientError(400, "target must be aws, azure, or gcp")
	}
	if !validFederationFormats[format] {
		return "", "", "", NewClientError(400, "format must be one of: cli, bundle, cfn, bicep, arm")
	}
	return target, source, format, nil
}

// shellEscape escapes a string for safe use inside a double-quoted bash argument.
// It escapes characters that have special meaning in double-quoted strings: \, $, `, ".
func shellEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`)
	return r.Replace(s)
}

// hclEscape escapes a string for use inside a double-quoted HCL string. HCL
// also starts an interpolation at "${" and a template directive at "%{", so
// both are doubled into their literal "$${" and "%%{" forms (#952). Line
// breaks are not escaped: renderTemplate refuses any value holding one
// (checkRenderDataSingleLine, #970).
func hclEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "${", "$${", "%{", "%%{").Replace(s)
}

// hclEscapeData returns a copy of data with every string field HCL-escaped,
// for rendering the bundle's tfvars. Most fields are server-derived, but
// ContactEmail is the session user's email, which only has to pass
// mail.ParseAddress and so can carry quotes and "${" in a quoted local part
// (#952). It walks the struct by reflection so a new field cannot be missed.
func hclEscapeData(data federationIaCData) federationIaCData {
	v := reflect.ValueOf(&data).Elem()
	for i := 0; i < v.NumField(); i++ {
		if f := v.Field(i); f.Kind() == reflect.String {
			f.SetString(hclEscape(f.String()))
		}
	}
	return data
}

// validFederationTargets is the allowlist of valid target cloud providers.
var validFederationTargets = map[string]bool{"aws": true, "azure": true, "gcp": true}

// singleFileSpec returns the template path, output filename, and content-type for a
// single-file IaC download. Zip formats are intercepted earlier by formatNeedsZip
// in getFederationIaC and never reach this function. Only "cli" is a valid single-file
// format after the tfvars-only path was removed.
func singleFileSpec(target, source, format, slug string) (tmplPath, filename, contentType string, err error) {
	if format == "cli" {
		return cliScriptSpec(target, source, slug)
	}
	return "", "", "", NewClientError(400, "unsupported format: "+format)
}

// cliScriptSpec returns the CLI shell-script template path + filename for the
// target/source combination. All CLI scripts are rendered with shell-escaped data.
func cliScriptSpec(target, source, slug string) (tmplPath, filename, contentType string, err error) {
	const ct = "text/x-shellscript"
	switch {
	case target == "aws" && source == "aws":
		return "templates/aws-cross-account-cli.sh.tmpl", slug + "-aws-cross-account-cli.sh", ct, nil
	case target == "aws":
		return "templates/aws-wif-cli.sh.tmpl", slug + "-aws-wif-cli.sh", ct, nil
	case target == "azure":
		return "templates/azure-wif-cli.sh.tmpl", slug + "-azure-wif-cli.sh", ct, nil
	case target == "gcp" && source == "gcp":
		return "templates/gcp-sa-impersonation-cli.sh.tmpl", slug + "-gcp-sa-impersonation-cli.sh", ct, nil
	case target == "gcp":
		return "templates/gcp-wif-cli.sh.tmpl", slug + "-gcp-wif-cli.sh", ct, nil
	default:
		return "", "", "", NewClientError(400, fmt.Sprintf("unsupported target/source combination: %s/%s", target, source))
	}
}

// buildFederationBundle creates a zip bundle containing:
//   - The generated .tfvars file
//   - The Terraform module files (main.tf, variables.tf, outputs.tf)
//   - For aws-target: the CloudFormation template + parameters JSON + deploy script
//
// Returns the raw zip bytes and output filename. base64 wrapping happens in
// buildZipResponse.
func buildFederationBundle(data federationIaCData, target, source, slug string) (zipData []byte, outputName string, err error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	if err := addBundleTerraform(zw, data, target, source, slug); err != nil {
		return nil, "", err
	}
	if err := addBundleCFN(zw, data, target, source, slug); err != nil {
		return nil, "", err
	}

	providerNote, noteErr := bundleProviderReadmeSection(target, source)
	if noteErr != nil {
		return nil, "", fmt.Errorf("bundle: %w", noteErr)
	}
	readme := buildBundleReadme(data, target, source) + providerNote
	if err := addStringToZip(zw, "README.txt", readme); err != nil {
		return nil, "", fmt.Errorf("bundle: write readme: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, "", fmt.Errorf("bundle: finalize zip: %w", err)
	}

	return buf.Bytes(), bundleZipName(target, source, slug), nil
}

// buildCFNZip creates a self-contained CloudFormation zip with template.yaml,
// the parameters JSON, and deploy-cfn.sh. Returns raw zip bytes + filename.
func buildCFNZip(data federationIaCData, target, source, slug string) (zipData []byte, outputName string, err error) {
	if target != "aws" {
		return nil, "", NewClientError(400, "format=cfn requires target=aws")
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if err := writeCFNFiles(zw, data, source, slug); err != nil {
		return nil, "", err
	}
	if err := zw.Close(); err != nil {
		return nil, "", fmt.Errorf("cfn: finalize zip: %w", err)
	}
	zipName := slug + "-aws-wif-cfn.zip"
	if source == "aws" {
		zipName = slug + "-aws-cross-account-cfn.zip"
	}
	return buf.Bytes(), zipName, nil
}

// azureTemplateName maps a format to the static template filename inside
// iac/federation/azure-target/bicep/. Returns "" for unknown formats.
func azureTemplateName(format string) string {
	switch format {
	case "bicep":
		return "azure-wif.bicep"
	case "arm":
		return "azure-wif.arm.json"
	}
	return ""
}

// buildAzureTemplateZip creates a zip containing the Azure Bicep or ARM template,
// a rendered parameters file, the deploy-azure.sh wrapper script, and a README
// describing the two-step flow (run azure-wif-cli.sh first to create the
// identity, then deploy this template to assign the Reservation Purchaser role).
//
// format must be "bicep" or "arm". target must be "azure".
func buildAzureTemplateZip(format string, data federationIaCData, target, slug string) (zipData []byte, outputName string, err error) {
	if target != "azure" {
		return nil, "", NewClientError(400, "format="+format+" requires target=azure")
	}
	templateName := azureTemplateName(format)
	if templateName == "" {
		return nil, "", NewClientError(400, "unsupported azure template format: "+format)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if err := writeAzureTemplateFiles(zw, data, format, templateName); err != nil {
		return nil, "", err
	}
	if err := zw.Close(); err != nil {
		return nil, "", fmt.Errorf("azure %s: finalize zip: %w", format, err)
	}
	return buf.Bytes(), slug + "-azure-wif-" + format + ".zip", nil
}

// writeAzureTemplateFiles reads the static Azure template, renders the
// parameters file, deploy script, and README, then writes all four into the zip.
// The deploy script is marked executable (mode 0755) in the zip header.
func writeAzureTemplateFiles(zw *zip.Writer, data federationIaCData, format, templateName string) error {
	templateBytes, err := cudlyiac.Modules.ReadFile("federation/azure-target/bicep/" + templateName)
	if err != nil {
		return fmt.Errorf("azure %s: read template: %w", format, err)
	}
	// The deploy script embeds the session user's email in a double-quoted
	// bash string, so render it shell-escaped like the CLI and CFN scripts
	// (#958). The params JSON interpolates no fields and stays unescaped.
	deployScript, err := renderTemplate("templates/azure-wif-bicep-deploy.sh.tmpl", shellEscapeData(data))
	if err != nil {
		return fmt.Errorf("azure %s: render deploy script: %w", format, err)
	}
	paramsJSON, err := renderTemplate("templates/azure-wif-bicep-params.json.tmpl", data)
	if err != nil {
		return fmt.Errorf("azure %s: render params: %w", format, err)
	}

	// Non-executable entries.
	entries := []struct {
		name    string
		content []byte
	}{
		{templateName, templateBytes},
		{"azure-wif-bicep-params.json", []byte(paramsJSON)},
		{"README.txt", []byte(buildAzureTemplateReadme(data, format))},
	}
	for _, e := range entries {
		if err = addBytesToZip(zw, e.name, e.content); err != nil {
			return fmt.Errorf("azure %s: write %s: %w", format, e.name, err)
		}
	}

	// deploy-azure.sh must be executable so customers can run it directly.
	if err = addExecBytesToZip(zw, "deploy-azure.sh", []byte(deployScript)); err != nil {
		return fmt.Errorf("azure %s: write deploy-azure.sh: %w", format, err)
	}
	return nil
}

func buildAzureTemplateReadme(data federationIaCData, format string) string {
	var sb strings.Builder
	sb.WriteString("CUDly Azure Federation — ")
	if format == "bicep" {
		sb.WriteString("Bicep deployment\n")
		sb.WriteString("===================================\n\n")
	} else {
		sb.WriteString("ARM deployment\n")
		sb.WriteString("================================\n\n")
	}
	fmt.Fprintf(&sb, "Account : %s (%s)\n\n", data.AccountName, data.AccountExternalID)
	sb.WriteString("The deploy script creates an Azure AD App Registration with a federated\n")
	sb.WriteString("identity credential bound to CUDly's OIDC issuer, then deploys the role\n")
	sb.WriteString("assignment template. No certificate or secret is created.\n\n")
	sb.WriteString("One-step deployment:\n\n")
	sb.WriteString("  bash deploy-azure.sh [--location <region>] [--template " + format + "]\n\n")
	sb.WriteString("Registration with CUDly runs automatically after deployment.\n")
	sb.WriteString("Set CUDLY_CONTACT_EMAIL to override the pre-filled contact email.\n\n")
	sb.WriteString("After deployment, set azure_auth_mode=workload_identity_federation in CUDly.\n")
	return sb.String()
}

// ---------------------------------------------------------------------------
// CF params JSON helper — uses encoding/json for correct escaping
// ---------------------------------------------------------------------------

type cfParam struct {
	ParameterKey   string `json:"ParameterKey"`
	ParameterValue string `json:"ParameterValue"`
}

// buildCFParamsJSON produces the CloudFormation parameter overrides JSON using
// encoding/json, which correctly escapes special characters in values.
//
// Dispatches on source: "aws" → cross-account params (SourceAccountID, ExternalID);
// anything else → AWS WIF params (OIDC values).
func buildCFParamsJSON(data federationIaCData, source string) (string, error) {
	var params []cfParam
	if source == "aws" {
		params = []cfParam{
			{ParameterKey: "SourceAccountID", ParameterValue: data.SourceAccountID},
			{ParameterKey: "ExternalID", ParameterValue: uuid.New().String()},
			{ParameterKey: "RoleName", ParameterValue: "CUDly-" + data.AccountSlug},
			{ParameterKey: "CUDlyAPIURL", ParameterValue: data.CUDlyAPIURL},
			{ParameterKey: "ContactEmail", ParameterValue: ""},
		}
	} else {
		params = []cfParam{
			{ParameterKey: "OIDCIssuerURL", ParameterValue: data.OIDCIssuerURL},
			{ParameterKey: "OIDCAudience", ParameterValue: data.OIDCAudience},
			{ParameterKey: "OIDCSubjectClaim", ParameterValue: data.OIDCSubjectClaim},
			{ParameterKey: "RoleName", ParameterValue: "CUDly-" + data.AccountSlug},
		}
	}
	out, err := json.MarshalIndent(params, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal cf params: %w", err)
	}
	return string(out) + "\n", nil
}

// ---------------------------------------------------------------------------
// Zip helpers
// ---------------------------------------------------------------------------

func addBytesToZip(zw *zip.Writer, name string, content []byte) error {
	f, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = f.Write(content)
	return err
}

// addExecBytesToZip writes content to the zip with Unix mode 0755 so that
// customers can run the resulting shell script without an explicit chmod.
func addExecBytesToZip(zw *zip.Writer, name string, content []byte) error {
	hdr := &zip.FileHeader{
		Name:   name,
		Method: zip.Deflate,
	}
	hdr.SetMode(0755)
	f, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = f.Write(content)
	return err
}

func addStringToZip(zw *zip.Writer, name, content string) error {
	return addBytesToZip(zw, name, []byte(content))
}
