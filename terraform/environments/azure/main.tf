# Azure Development Environment
# Orchestrates all Azure modules for CUDly deployment

terraform {
  required_version = ">= 1.10.0"

  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 3.117"
    }
    azuread = {
      source  = "hashicorp/azuread"
      version = "~> 2.53"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.3"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.23"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 2.11"
    }
    http = {
      source  = "hashicorp/http"
      version = "~> 3.4"
    }
    null = {
      source  = "hashicorp/null"
      version = "~> 3.2"
    }
    external = {
      source  = "hashicorp/external"
      version = "~> 2.3"
    }
  }

}

provider "azurerm" {
  features {
    key_vault {
      purge_soft_delete_on_destroy    = false
      recover_soft_deleted_key_vaults = true
    }
    resource_group {
      # Default to true so `terraform destroy` / `az group delete` cannot
      # wipe a populated resource group without explicit operator override.
      # Dev environments opt out via tfvars (see variables.tf).
      prevent_deletion_if_contains_resources = var.prevent_resource_group_deletion
    }
  }

  subscription_id = var.subscription_id
}

# Kubernetes and Helm providers for AKS
# The module's kube_config output is the raw kubeconfig YAML string, so its
# attributes cannot be read; use the dedicated host/CA outputs instead (the
# azurerm values are base64-encoded). Authentication is exec-based kubelogin
# (Entra ID, Azure CLI login): AKS runs with local_account_disabled = true by
# default, so there is no client certificate/key. The deploy identity needs an
# Azure RBAC role on the cluster (e.g. "Azure Kubernetes Service RBAC Cluster
# Admin") and kubelogin must be on PATH. The server-id is the public AKS
# Entra server application ID. With compute_platform != "aks" the providers
# stay unconfigured and no kubernetes/helm resource is planned.
locals {
  aks_host = try(module.compute_aks[0].host, "https://localhost")
  aks_ca   = try(base64decode(module.compute_aks[0].cluster_ca_certificate), "")
  aks_exec_args = [
    "get-token", "--login", "azurecli",
    "--server-id", "6dae42f8-4368-4678-94ff-3960e28e3630",
  ]
}

provider "kubernetes" {
  host                   = local.aks_host
  cluster_ca_certificate = local.aks_ca

  exec {
    api_version = "client.authentication.k8s.io/v1beta1"
    command     = "kubelogin"
    args        = local.aks_exec_args
  }
}

provider "helm" {
  kubernetes {
    host                   = local.aks_host
    cluster_ca_certificate = local.aks_ca

    exec {
      api_version = "client.authentication.k8s.io/v1beta1"
      command     = "kubelogin"
      args        = local.aks_exec_args
    }
  }
}

# Unique suffix to prevent naming conflicts (Key Vault, ACR are globally unique)
resource "random_id" "suffix" {
  byte_length = 4
}

# ==============================================
# Local Variables
# ==============================================

locals {
  app_name = "${var.project_name}-${var.environment}-${random_id.suffix.hex}"

  common_tags = merge(var.tags, {
    Environment = var.environment
    ManagedBy   = "terraform"
    Project     = "CUDly"
    CostCenter  = var.cost_center
  })

  # Dashboard URL for password reset emails
  # Use custom domain if configured, otherwise will be set after deployment
  dashboard_url = length(var.frontend_domain_names) > 0 ? "https://${var.frontend_domain_names[0]}" : ""

  # Container image parsing (simplifies module calls)
  full_image_uri = var.enable_docker_build ? module.build[0].image_uri : var.image_uri
  image_name     = split(":", local.full_image_uri)[0]
  image_tag      = try(split(":", local.full_image_uri)[1], "latest")
}

# ==============================================
# Resource Group
# ==============================================

# ==============================================
# Provider Registrations
# ==============================================

resource "azurerm_resource_provider_registration" "container_apps" {
  name = "Microsoft.App"
}

# ==============================================
# Resource Group
# ==============================================

resource "azurerm_resource_group" "main" {
  name     = "${local.app_name}-rg"
  location = var.location

  tags = local.common_tags
}
