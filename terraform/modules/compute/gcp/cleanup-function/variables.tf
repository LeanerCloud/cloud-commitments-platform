variable "project_id" {
  description = "GCP project ID"
  type        = string
}

variable "region" {
  description = "GCP region"
  type        = string
}

variable "function_name" {
  description = "Name of the Cloud Function"
  type        = string
  default     = "cudly-cleanup"
}

variable "db_host" {
  description = "Database host (Cloud SQL connection name or private IP)"
  type        = string
}

variable "db_password_secret_id" {
  description = "Secret Manager secret ID containing the database password"
  type        = string
}

variable "vpc_connector" {
  description = "VPC connector for private Cloud SQL access"
  type        = string
  default     = ""
}

variable "schedule" {
  description = "Cloud Scheduler schedule (cron format)"
  type        = string
  default     = "0 2 * * *"
}

variable "labels" {
  description = "Labels to apply to all resources"
  type        = map(string)
  default     = {}
}

variable "source_object_name" {
  description = <<-EOT
    Name of the object already present in the function-source bucket
    (google_storage_bucket.function_source) containing the packaged Cloud
    Function source archive. Supplied by the build/CI pipeline, which must
    upload the object before this module is applied -- the same
    externally-supplied-artifact pattern terraform/modules/build establishes
    for container images (see its registry_url/custom_image_tag variables).
    Embed a content hash or build ID in the object name (e.g.
    "cleanup-<sha256>.zip") so a source change produces a new name and
    therefore a new Cloud Function revision; reusing the same name for
    changed content is a no-op deploy because Cloud Functions deploys by
    object identity, not by content diffing.
  EOT
  type        = string
}
