variable "subscription_id" {
  description = "Azure subscription ID. Auto-detected from CLI context when empty."
  type        = string
  default     = ""
}

variable "tenant_id" {
  description = "Azure AD tenant ID. Auto-detected from CLI context when empty."
  type        = string
  default     = ""
}

variable "app_display_name" {
  description = "Display name for the Azure AD App Registration."
  type        = string
  default     = "CUDly"
}

variable "cudly_issuer_url" {
  description = "CUDly OIDC issuer URL (e.g. https://cudly.example.com/oidc). Azure AD fetches JWKS from this issuer to verify client assertion JWTs."
  type        = string

  # CUDly contract, mirrored by the two azure-wif shell templates.
  validation {
    condition = var.cudly_issuer_url == null ? false : (
      can(regex("^https://[A-Za-z0-9.-]+(:[0-9]+)?(/[A-Za-z0-9._~%/-]*)?$", var.cudly_issuer_url)) &&
      !endswith(var.cudly_issuer_url, "/")
    )
    error_message = "Use a CUDly HTTPS issuer URL without a trailing slash, query, fragment or userinfo."
  }
}

variable "cudly_federated_subject" {
  description = "Subject claim in the client assertion JWT. Must match what CUDly signs."
  type        = string
  default     = "cudly-controller"

  validation {
    condition     = var.cudly_federated_subject == null ? false : can(regex("^[^ \\t\\n\\r\\x0b\\f$*]+$", var.cudly_federated_subject))
    error_message = "Subject must be nonempty without ASCII whitespace, dollar signs or asterisks."
  }
}

variable "cudly_federated_audience" {
  description = "Audience for the federated identity credential."
  type        = string
  default     = "api://AzureADTokenExchange"

  validation {
    condition     = var.cudly_federated_audience == null ? false : can(regex("^[^ \\t\\n\\r\\x0b\\f$*]+$", var.cudly_federated_audience))
    error_message = "Audience must be nonempty without ASCII whitespace, dollar signs or asterisks."
  }
}

variable "cudly_api_url" {
  description = "CUDly API base URL for automatic account registration. Leave empty to skip registration."
  type        = string
  default     = ""
}

variable "account_name" {
  description = "Human-readable name for this account in CUDly."
  type        = string
  default     = ""
}

variable "contact_email" {
  description = "Contact email for registration notifications."
  type        = string
  default     = ""
}
