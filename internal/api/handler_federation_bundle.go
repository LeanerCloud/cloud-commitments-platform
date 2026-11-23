package api

import (
	"archive/zip"
	"fmt"
	"strings"

	cudlyiac "github.com/LeanerCloud/cloud-commitments-platform/iac"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/iacbundle"
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
