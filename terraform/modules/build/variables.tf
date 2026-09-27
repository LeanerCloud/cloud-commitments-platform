# Docker Build Module Variables

variable "registry_url" {
  description = "Docker registry URL (e.g., 123456789012.dkr.ecr.us-east-1.amazonaws.com)"
  type        = string
}

variable "image_name" {
  description = "Docker image name (e.g., cudly-lambda-dev)"
  type        = string
}

variable "source_path" {
  description = "Path to source code directory containing Dockerfile"
  type        = string
  default     = "../../../.."
}

variable "platform" {
  description = "Target platform for Docker build (e.g. linux/amd64, linux/arm64). Empty string uses native platform."
  type        = string
  default     = ""
}

variable "custom_image_tag" {
  description = "Custom image tag (defaults to git-commit-timestamp)"
  type        = string
  default     = ""
}

variable "skip_docker_build" {
  description = "Skip Docker build (useful for infrastructure-only changes)"
  type        = bool
  default     = false
}

variable "extra_build_args" {
  description = "Extra arguments to pass to docker build. Passed through the local-exec provisioner's environment (not interpolated into the script), then whitespace-split unquoted (no shell quoting is honored): a value like \"--build-arg LABEL=hello world\" splits into two docker buildx arguments, not one. No caller sets this today; if a value ever needs an embedded space, extend the module to accept a list(string) instead."
  type        = string
  default     = ""
}

variable "registry_login_command" {
  description = "Command to authenticate with registry (e.g., az acr login --name ..., aws ecr get-login-password | docker login ..., gcloud auth configure-docker ...). Runs verbatim as shell input. Must be an identity-based login (the CLI resolves/mints the credential itself); never embed a static, long-lived credential literal (a password, API key, or JSON key) in this value; it is not redacted in terraform plan/apply output, and marking it sensitive would suppress this resource's entire local-exec log (build/push progress, docker error output), which deploy-*.yml workflows tee and grep to detect build failures."
  type        = string
}

variable "cleanup_old_images" {
  description = "Clean up old Docker images after push"
  type        = bool
  default     = true
}
