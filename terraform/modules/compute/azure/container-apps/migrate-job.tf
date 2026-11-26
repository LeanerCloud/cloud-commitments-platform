# One-shot database migration runner (#1966, mirrors the AWS Lambda migrate
# event from #31).
#
# A manually triggered Container Apps job in the same environment, with the same
# image and the same user-assigned identity as the app. That identity already
# pulls from the registry (AcrPull) and reads the database and admin password
# secrets, so no role assignment is added here. The deploy workflow starts one
# execution after `terraform apply` (the job then points at the new image),
# waits for it and fails the deploy when it does not succeed. The deploy
# identity needs the Microsoft.App/jobs actions in
# ci-cd-permissions/locals_compute.tf (a human bootstrap re-apply).
#
# The command bypasses /entrypoint.sh on purpose: the entrypoint starts the
# HTTP server, and the lazy auto-migrate path must not run inside the job. The
# lazy path (DB_AUTO_MIGRATE on the app) stays until the owner has verified a
# real apply and flips auto_migrate (#31 D4).

resource "azurerm_container_app_job" "migrate" {
  name                         = "${var.app_name}-migrate"
  location                     = var.location
  resource_group_name          = var.resource_group_name
  container_app_environment_id = azurerm_container_app_environment.main.id

  # The same bound as the AWS step (20 minutes); one attempt, so a failure is
  # seen by the deploy instead of being retried behind its back.
  replica_timeout_in_seconds = 1200
  replica_retry_limit        = 0

  manual_trigger_config {
    parallelism              = 1
    replica_completion_count = 1
  }

  identity {
    type         = "UserAssigned"
    identity_ids = [azurerm_user_assigned_identity.container_app.id]
  }

  registry {
    server   = var.registry_server
    identity = azurerm_user_assigned_identity.container_app.id
  }

  template {
    container {
      name    = "migrate"
      image   = var.image_uri
      cpu     = 0.5
      memory  = "1Gi"
      command = ["/app/cudly"]
      args    = ["--migrate"]

      # Only what the migrator reads: database connection, migrations path,
      # admin bootstrap and the secret provider.
      dynamic "env" {
        for_each = {
          ENVIRONMENT           = var.environment
          DB_HOST               = var.database_host
          DB_PORT               = "5432"
          DB_NAME               = var.database_name
          DB_USER               = var.database_username
          DB_PASSWORD_SECRET    = var.database_password_secret_name
          DB_SSL_MODE           = "require"
          DB_CONNECT_TIMEOUT    = "8s"
          DB_MIGRATIONS_PATH    = "/app/migrations"
          ADMIN_EMAIL           = var.admin_email
          ADMIN_PASSWORD_SECRET = var.admin_password_secret_name
          SECRET_PROVIDER       = "azure"
          AZURE_CLIENT_ID       = azurerm_user_assigned_identity.container_app.client_id
          AZURE_SUBSCRIPTION_ID = var.subscription_id
          AZURE_TENANT_ID       = data.azurerm_subscription.current.tenant_id
          AZURE_KEY_VAULT_URL   = var.key_vault_uri
          AZURE_REGION          = var.location
        }
        content {
          name  = env.key
          value = env.value
        }
      }
    }
  }

  tags = merge(var.tags, {
    managed_by = "terraform"
  })

  # AcrPull must exist before the first execution pulls the image.
  depends_on = [azurerm_role_assignment.acr_pull]
}
