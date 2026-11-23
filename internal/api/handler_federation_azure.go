package api

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"

	cudlyiac "github.com/LeanerCloud/cloud-commitments-platform/iac"
)

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
