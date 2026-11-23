# One-shot database migration runner (#1966, mirrors the AWS Lambda migrate
# event from #31).
#
# The job runs the same image as the service with `/app/cudly --migrate`. The
# deploy workflow executes it after `terraform apply` (so the job already points
# at the new image) and fails the deploy if the execution fails. It reuses the
# service's service account, so it already holds the Cloud SQL client role and
# the secret accessors it needs (DB password, admin password); no new IAM is
# granted here. The deploy identity starts executions with roles/run.admin from
# ci-cd-permissions.
#
# The command bypasses /entrypoint.sh on purpose: the entrypoint starts the
# HTTP server, and the lazy auto-migrate path must not run inside the job.
# The lazy path (DB_AUTO_MIGRATE on the service) stays until the owner has
# verified a real apply and flips auto_migrate (#31 D4).

resource "google_cloud_run_v2_job" "migrate" {
  name     = "${var.service_name}-migrate"
  location = var.region
  project  = var.project_id

  template {
    # The migrator is idempotent but a failed run should be seen, not retried
    # behind the deploy's back.
    task_count  = 1
    parallelism = 1

    template {
      service_account = google_service_account.cloud_run.email
      max_retries     = 0
      timeout         = "1200s" # 20 min, the same bound as the AWS step

      dynamic "vpc_access" {
        for_each = var.vpc_connector_id != null ? [var.vpc_connector_id] : []
        content {
          connector = vpc_access.value
          egress    = var.vpc_egress_mode
        }
      }

      containers {
        image   = var.image_uri
        command = ["/app/cudly"]
        args    = ["--migrate"]

        resources {
          limits = {
            cpu    = "1"
            memory = "512Mi"
          }
        }

        # Only what the migrator reads: database connection, migrations path,
        # admin bootstrap and the secret provider.
        dynamic "env" {
          for_each = {
            ENVIRONMENT           = var.environment
            DB_HOST               = var.database_host
            DB_PORT               = "5432"
            DB_NAME               = var.database_name
            DB_USER               = var.database_username
            DB_PASSWORD_SECRET    = var.database_password_secret_id
            DB_SSL_MODE           = "require"
            DB_CONNECT_TIMEOUT    = "8s"
            DB_MIGRATIONS_PATH    = "/app/migrations"
            ADMIN_EMAIL           = var.admin_email
            ADMIN_PASSWORD_SECRET = var.admin_password_secret_name
            SECRET_PROVIDER       = "gcp"
            GCP_PROJECT_ID        = var.project_id
            GCP_REGION            = var.region
          }
          content {
            name  = env.key
            value = env.value
          }
        }
      }
    }
  }

  labels = merge(var.labels, {
    environment = var.environment
    managed_by  = "terraform"
  })

  depends_on = [
    google_project_iam_member.cloud_sql_client,
    google_secret_manager_secret_iam_member.db_password_reader,
  ]
}
