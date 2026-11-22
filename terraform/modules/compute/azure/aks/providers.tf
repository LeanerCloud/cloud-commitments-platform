# Terraform and Provider Configuration for AKS Module
# Note: Provider blocks have been removed to allow this module to be used with count/for_each
# Providers (azurerm, kubernetes, helm) should be configured at the root module level

terraform {
  required_version = ">= 1.6.0"

  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 3.117"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.23"
      # Provider will be configured at root level after cluster creation
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.11"
      # Provider will be configured at root level after cluster creation
    }
    time = {
      source  = "hashicorp/time"
      version = "~> 0.11"
      # Used by the time_sleep resource that waits for the NGINX LB IP to
      # be provisioned by Azure (async, 2–5 min) before the
      # kubernetes_service data source reads it.
    }
  }
}

# Note: Provider configurations removed from module
# The kubernetes/helm providers for this cluster are configured at the root level
# (terraform/environments/azure/main.tf) from this module's host and
# cluster_ca_certificate outputs with a kubelogin exec block. Do not read
# kube_config: it is the raw kubeconfig YAML string, not an object.
