// Regression test for issue #125: google_container_cluster.main declared no
// private_cluster_config and no master_authorized_networks_config, so the
// GKE control plane got a public endpoint accepting connections from
// 0.0.0.0/0 and nodes got public IPs.
package gcp_test

import (
	"os"
	"regexp"
	"testing"
)

func TestGKEClusterIsPrivateWithAuthorizedNetworks(t *testing.T) {
	data, err := os.ReadFile("gke/main.tf")
	if err != nil {
		t.Fatalf("reading gke/main.tf: %v", err)
	}
	content := string(data)

	if !regexp.MustCompile(`private_cluster_config\s*\{[^}]*enable_private_nodes\s*=\s*var\.enable_private_nodes`).MatchString(content) {
		t.Error(`google_container_cluster.main must have a private_cluster_config block with enable_private_nodes wired from a variable (issue #125); nodes were getting public IPs`)
	}

	if !regexp.MustCompile(`master_authorized_networks_config\s*\{[^}]*gcp_public_cidrs_access_enabled\s*=\s*var\.gcp_public_cidrs_access_enabled`).MatchString(content) {
		t.Error(`google_container_cluster.main's master_authorized_networks_config must set gcp_public_cidrs_access_enabled from a variable (issue #125 round 2); the provider defaults this to true, so an empty cidr_blocks allowlist alone does NOT deny all external access -- any Google Cloud public IP could still reach the endpoint`)
	}

	if !regexp.MustCompile(`private_cluster_config\s*\{[^}]*master_ipv4_cidr_block\s*=\s*var\.master_ipv4_cidr_block`).MatchString(content) {
		t.Error(`google_container_cluster.main's private_cluster_config must set master_ipv4_cidr_block from a variable (issue #125 round 2); the google provider rejects enable_private_nodes=true on a Standard cluster at apply time (not caught by validate/plan) unless master_ipv4_cidr_block or private_endpoint_subnetwork is set`)
	}

	varsData, err := os.ReadFile("gke/variables.tf")
	if err != nil {
		t.Fatalf("reading gke/variables.tf: %v", err)
	}
	vars := string(varsData)

	if !regexp.MustCompile(`variable\s+"enable_private_nodes"\s*\{[^}]*default\s*=\s*true`).MatchString(vars) {
		t.Error(`var.enable_private_nodes must default to true, so a caller who does not override it gets private nodes`)
	}

	if !regexp.MustCompile(`variable\s+"master_authorized_networks"[\s\S]*?contains\(\[for n in var\.master_authorized_networks[^\]]*\],\s*"0\.0\.0\.0/0"\)`).MatchString(vars) {
		t.Error(`var.master_authorized_networks must validate against "0.0.0.0/0", so the control plane's public endpoint can't be silently reopened to the whole internet`)
	}

	if !regexp.MustCompile(`variable\s+"gcp_public_cidrs_access_enabled"\s*\{[^}]*default\s*=\s*false`).MatchString(vars) {
		t.Error(`var.gcp_public_cidrs_access_enabled must default to false, or the empty master_authorized_networks default does not actually deny all external access`)
	}

	if !regexp.MustCompile(`variable\s+"master_ipv4_cidr_block"\s*\{[\s\S]*?tonumber\(split\("/",\s*var\.master_ipv4_cidr_block\)\[1\]\)\s*==\s*28`).MatchString(vars) {
		t.Error(`var.master_ipv4_cidr_block must validate that its value is a /28 CIDR`)
	}
}
