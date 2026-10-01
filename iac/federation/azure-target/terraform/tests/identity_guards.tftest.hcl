mock_provider "azuread" {}
mock_provider "http" {}
mock_provider "azurerm" {
  mock_data "azurerm_subscription" {
    defaults = {
      id              = "/subscriptions/11111111-1111-1111-1111-111111111111"
      subscription_id = "11111111-1111-1111-1111-111111111111"
      tenant_id       = "22222222-2222-2222-2222-222222222222"
    }
  }
}

variables {
  subscription_id  = "11111111-1111-1111-1111-111111111111"
  tenant_id        = "22222222-2222-2222-2222-222222222222"
  cudly_issuer_url = "https://cudly.example.com/oidc"
  cudly_api_url    = ""
}

run "defaults" {
  command = plan
  assert {
    condition = (
      azuread_application_federated_identity_credential.cudly.issuer == var.cudly_issuer_url &&
      azuread_application_federated_identity_credential.cudly.subject == "cudly-controller" &&
      azuread_application_federated_identity_credential.cudly.audiences == tolist(["api://AzureADTokenExchange"])
    )
    error_message = "Default identity must be preserved exactly."
  }
}

run "literal_claims" {
  command = plan
  variables {
    cudly_issuer_url         = "https://CUDly.example.com:8443/a_b/~v1%20/oidc"
    cudly_federated_subject  = "quote\"backslash\\backtick`apostrophe':é"
    cudly_federated_audience = "quote\"backslash\\backtick`apostrophe':é"
  }
  assert {
    condition = (
      azuread_application_federated_identity_credential.cudly.issuer == var.cudly_issuer_url &&
      azuread_application_federated_identity_credential.cudly.subject == var.cudly_federated_subject &&
      azuread_application_federated_identity_credential.cudly.audiences == tolist([var.cudly_federated_audience])
    )
    error_message = "Literal identity bytes and singleton audience must be preserved."
  }
}

run "invalid_claim_0" {
  command = plan
  variables {
    cudly_federated_subject  = null
    cudly_federated_audience = null
  }
  expect_failures = [var.cudly_federated_subject, var.cudly_federated_audience]
}

run "invalid_claim_1" {
  command = plan
  variables {
    cudly_federated_subject  = ""
    cudly_federated_audience = ""
  }
  expect_failures = [var.cudly_federated_subject, var.cudly_federated_audience]
}

run "invalid_claim_2" {
  command = plan
  variables {
    cudly_federated_subject  = "a b"
    cudly_federated_audience = "a b"
  }
  expect_failures = [var.cudly_federated_subject, var.cudly_federated_audience]
}

run "invalid_claim_3" {
  command = plan
  variables {
    cudly_federated_subject  = "a\tb"
    cudly_federated_audience = "a\tb"
  }
  expect_failures = [var.cudly_federated_subject, var.cudly_federated_audience]
}

run "invalid_claim_4" {
  command = plan
  variables {
    cudly_federated_subject  = "a\nb"
    cudly_federated_audience = "a\nb"
  }
  expect_failures = [var.cudly_federated_subject, var.cudly_federated_audience]
}

run "invalid_claim_5" {
  command = plan
  variables {
    cudly_federated_subject  = "a\rb"
    cudly_federated_audience = "a\rb"
  }
  expect_failures = [var.cudly_federated_subject, var.cudly_federated_audience]
}

run "invalid_claim_6" {
  command = plan
  variables {
    cudly_federated_subject  = "a\u000bb"
    cudly_federated_audience = "a\u000bb"
  }
  expect_failures = [var.cudly_federated_subject, var.cudly_federated_audience]
}

run "invalid_claim_7" {
  command = plan
  variables {
    cudly_federated_subject  = "a\u000cb"
    cudly_federated_audience = "a\u000cb"
  }
  expect_failures = [var.cudly_federated_subject, var.cudly_federated_audience]
}

run "invalid_claim_8" {
  command = plan
  variables {
    cudly_federated_subject  = "a$b"
    cudly_federated_audience = "a$b"
  }
  expect_failures = [var.cudly_federated_subject, var.cudly_federated_audience]
}

run "invalid_claim_9" {
  command = plan
  variables {
    cudly_federated_subject  = "a*b"
    cudly_federated_audience = "a*b"
  }
  expect_failures = [var.cudly_federated_subject, var.cudly_federated_audience]
}

run "invalid_issuer_0" {
  command = plan
  variables {
    cudly_issuer_url = null
  }
  expect_failures = [var.cudly_issuer_url]
}

run "invalid_issuer_1" {
  command = plan
  variables {
    cudly_issuer_url = ""
  }
  expect_failures = [var.cudly_issuer_url]
}

run "invalid_issuer_2" {
  command = plan
  variables {
    cudly_issuer_url = "http://example.com/oidc"
  }
  expect_failures = [var.cudly_issuer_url]
}

run "invalid_issuer_3" {
  command = plan
  variables {
    cudly_issuer_url = "https://"
  }
  expect_failures = [var.cudly_issuer_url]
}

run "invalid_issuer_4" {
  command = plan
  variables {
    cudly_issuer_url = "https://example.com/oidc/"
  }
  expect_failures = [var.cudly_issuer_url]
}

run "invalid_issuer_5" {
  command = plan
  variables {
    cudly_issuer_url = "https://user@example.com/oidc"
  }
  expect_failures = [var.cudly_issuer_url]
}

run "invalid_issuer_6" {
  command = plan
  variables {
    cudly_issuer_url = "https://example.com/?q=1"
  }
  expect_failures = [var.cudly_issuer_url]
}

run "invalid_issuer_7" {
  command = plan
  variables {
    cudly_issuer_url = "https://example.com/#f"
  }
  expect_failures = [var.cudly_issuer_url]
}

run "invalid_issuer_8" {
  command = plan
  variables {
    cudly_issuer_url = "https://example.com/a b"
  }
  expect_failures = [var.cudly_issuer_url]
}

run "invalid_issuer_9" {
  command = plan
  variables {
    cudly_issuer_url = "https://example.com/a\nb"
  }
  expect_failures = [var.cudly_issuer_url]
}

run "invalid_issuer_10" {
  command = plan
  variables {
    cudly_issuer_url = "https://example.com/a\"b"
  }
  expect_failures = [var.cudly_issuer_url]
}

run "invalid_issuer_11" {
  command = plan
  variables {
    cudly_issuer_url = "https://example.com/a\\b"
  }
  expect_failures = [var.cudly_issuer_url]
}
