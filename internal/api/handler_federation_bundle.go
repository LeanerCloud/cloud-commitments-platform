package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	cudlyiac "github.com/LeanerCloud/cloud-commitments-platform/iac"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/iacbundle"
	"github.com/google/uuid"
)

// addBundleTerraform adds the Terraform module files and generated .tfvars to the zip.
func addBundleTerraform(zw *zip.Writer, data federationIaCData, target, source, slug string) error {
	tfDir := bundleModuleDir(target, source) + "/terraform"
	if err := iacbundle.AddTerraformDir(zw, cudlyiac.Modules, tfDir, iacbundle.ExtraModules(target)); err != nil {
		return fmt.Errorf("bundle: terraform dir: %w", err)
	}
	tfvarsTmpl, tfvarsName := bundleTfvarsSpec(target, source, slug)
	tfvarsContent, err := renderTemplate(tfvarsTmpl, hclEscapeData(data))
	if err != nil {
		return fmt.Errorf("bundle: %w", err)
	}
	if err = addStringToZip(zw, tfvarsName, tfvarsContent); err != nil {
		return fmt.Errorf("bundle: write tfvars: %w", err)
	}
	return nil
}

// addBundleCFN adds CloudFormation files to the zip for AWS target bundles
// (both cross-account and WIF). Thin wrapper around writeCFNFiles.
func addBundleCFN(zw *zip.Writer, data federationIaCData, target, source, slug string) error {
	if target != "aws" {
		return nil
	}
	return writeCFNFiles(zw, data, source, slug)
}

// writeCFNFiles writes the CloudFormation template, parameters JSON, and deploy
// script into the given zip.Writer. Used by both addBundleCFN (for the bundle
// format) and buildCFNZip (for format=cfn).
//
// Dispatches on source: "aws" → cross-account IAM role template; anything else
// → AWS WIF template.
func writeCFNFiles(zw *zip.Writer, data federationIaCData, source, slug string) error {
	cfTemplatePath := "federation/aws-target/cloudformation/template.yaml"
	deployTmplPath := "templates/aws-cfn-deploy.sh.tmpl"
	if source == "aws" {
		cfTemplatePath = "federation/aws-cross-account/cloudformation/template.yaml"
		deployTmplPath = "templates/aws-cross-account-cfn-deploy.sh.tmpl"
	}
	cfTemplate, err := cudlyiac.Modules.ReadFile(cfTemplatePath)
	if err != nil {
		return fmt.Errorf("cfn: read cf template: %w", err)
	}
	if err = addBytesToZip(zw, "cloudformation/template.yaml", cfTemplate); err != nil {
		return fmt.Errorf("cfn: write cf template: %w", err)
	}
	cfParams, err := buildCFParamsJSON(data, source)
	if err != nil {
		return fmt.Errorf("cfn: %w", err)
	}
	if err = addStringToZip(zw, "cloudformation/"+slug+"-cf-params.json", cfParams); err != nil {
		return fmt.Errorf("cfn: write cf params: %w", err)
	}
	// Shell-escape template values before rendering the deploy script to prevent
	// injection via account names or OIDC URLs containing shell metacharacters.
	escapedData := shellEscapeData(data)
	deployScript, err := renderTemplate(deployTmplPath, escapedData)
	if err != nil {
		return fmt.Errorf("cfn: %w", err)
	}
	if err = addStringToZip(zw, "cloudformation/deploy-cfn.sh", deployScript); err != nil {
		return fmt.Errorf("cfn: write deploy script: %w", err)
	}
	return nil
}

// bundleModuleDir returns the embedded FS path for the Terraform module.
func bundleModuleDir(target, source string) string {
	switch {
	case target == "aws" && source == "aws":
		return "federation/aws-cross-account"
	case target == "aws":
		return "federation/aws-target"
	case target == "azure":
		return "federation/azure-target"
	case target == "gcp" && source == "gcp":
		return "federation/gcp-sa-impersonation"
	default:
		return "federation/gcp-target"
	}
}

// bundleTfvarsSpec returns the template path and zip destination name for the .tfvars file.
//
// The destination uses Terraform's `*.auto.tfvars` convention so the customer
// can run `terraform init && terraform apply` with no `-var-file=` flag —
// Terraform auto-loads any file matching that pattern from the current
// working directory. The descriptive `<slug>-<target>` prefix is preserved
// so admins downloading bundles for multiple accounts can keep them
// distinguishable on disk.
func bundleTfvarsSpec(target, source, slug string) (tmplPath, name string) {
	switch {
	case target == "aws" && source == "aws":
		return "templates/aws-cross-account.tfvars.tmpl", "terraform/" + slug + "-aws-cross-account.auto.tfvars"
	case target == "aws":
		return "templates/aws-wif.tfvars.tmpl", "terraform/" + slug + "-aws-wif.auto.tfvars"
	case target == "azure":
		return "templates/azure-wif.tfvars.tmpl", "terraform/" + slug + "-azure-wif.auto.tfvars"
	case target == "gcp" && source == "gcp":
		return "templates/gcp-sa-impersonation.tfvars.tmpl", "terraform/" + slug + "-gcp-sa-impersonation.auto.tfvars"
	default:
		return "templates/gcp-wif.tfvars.tmpl", "terraform/" + slug + "-gcp-wif.auto.tfvars"
	}
}

// bundleZipName returns the output filename for the zip bundle.
func bundleZipName(target, source, slug string) string {
	switch {
	case target == "aws" && source == "aws":
		return slug + "-aws-cross-account-bundle.zip"
	case target == "gcp" && source == "gcp":
		return slug + "-gcp-sa-impersonation-bundle.zip"
	default:
		return slug + "-" + target + "-federation-bundle.zip"
	}
}

func buildBundleReadme(data federationIaCData, target, source string) string {
	var sb strings.Builder
	sb.WriteString("CUDly Federation IaC Bundle\n")
	sb.WriteString("===========================\n\n")
	fmt.Fprintf(&sb, "Account : %s (%s)\n", data.AccountName, data.AccountExternalID)
	fmt.Fprintf(&sb, "Target  : %s\n", target)
	fmt.Fprintf(&sb, "Source  : %s\n\n", source)

	switch {
	case target == "aws" && source == "aws":
		sb.WriteString("Contents:\n  terraform/           - Cross-account IAM role Terraform module\n")
		sb.WriteString("  terraform/*.auto.tfvars - Pre-filled variable values (auto-loaded by Terraform)\n")
		sb.WriteString("  cloudformation/      - CloudFormation alternative\n\n")
		sb.WriteString("Deploy (Terraform):\n")
		sb.WriteString("  cd terraform && terraform init && terraform apply\n\n")
		sb.WriteString("Deploy (CloudFormation):\n")
		sb.WriteString("  cd cloudformation && bash deploy-cfn.sh --region <region>\n\n")
		sb.WriteString("After apply, set aws_auth_mode=role_arn and aws_role_arn in CUDly.\n")
	case target == "aws":
		sb.WriteString("Contents:\n  terraform/           - IAM OIDC provider + role Terraform module\n")
		sb.WriteString("  terraform/*.auto.tfvars - Pre-filled variable values (auto-loaded by Terraform)\n")
		sb.WriteString("  cloudformation/      - CloudFormation alternative\n\n")
		sb.WriteString("Deploy (Terraform):\n")
		sb.WriteString("  cd terraform && terraform init && terraform apply\n\n")
		sb.WriteString("Deploy (CloudFormation):\n")
		sb.WriteString("  cd cloudformation && bash deploy-cfn.sh --region <region>\n\n")
		sb.WriteString("After apply, set aws_auth_mode=workload_identity_federation and aws_role_arn in CUDly.\n")
	case target == "azure":
		sb.WriteString("Contents:\n  terraform/           - Azure App Registration + federated-identity-credential Terraform module\n")
		sb.WriteString("  terraform/modules/   - Custom reservation-purchaser role module called by terraform/main.tf\n")
		sb.WriteString("  terraform/*.auto.tfvars - Pre-filled variable values (auto-loaded by Terraform)\n\n")
		sb.WriteString("This uses true Workload Identity Federation — no certificate, no private key,\nno client secret is created or stored. CUDly's OIDC issuer signs a short-lived\nJWT on each Azure API call and Azure AD verifies it against the federated\ncredential provisioned here.\n\n")
		sb.WriteString("Deploy (Terraform):\n")
		sb.WriteString("  cd terraform && terraform init && terraform apply\n\n")
		sb.WriteString("After apply, set azure_auth_mode=workload_identity_federation in CUDly.\n")
	case target == "gcp" && source == "gcp":
		sb.WriteString("Contents:\n  terraform/           - Service account impersonation Terraform module\n")
		sb.WriteString("  terraform/*.auto.tfvars - Pre-filled variable values (auto-loaded by Terraform)\n\n")
		sb.WriteString("Deploy (Terraform):\n")
		sb.WriteString("  cd terraform && terraform init && terraform apply\n\n")
		sb.WriteString("After apply, set gcp_auth_mode=application_default in CUDly.\n")
	case target == "gcp":
		sb.WriteString("Contents:\n  terraform/           - Workload Identity Pool + provider Terraform module\n")
		sb.WriteString("  terraform/*.auto.tfvars - Pre-filled variable values (auto-loaded by Terraform)\n\n")
		sb.WriteString("Deploy (Terraform):\n")
		sb.WriteString("  cd terraform && terraform init && terraform apply\n\n")
		sb.WriteString("After apply, run the gcloud_command output to generate the WIF credential JSON,\nthen paste it into CUDly as gcp_workload_identity_config.\n")
	}

	return sb.String()
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
