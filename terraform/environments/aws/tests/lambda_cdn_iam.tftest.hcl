# Lambda behind CloudFront with enable_cdn = true (#16). The Function URL must
# be AWS_IAM, the distribution must carry the OAC, and CloudFront needs BOTH
# lambda:InvokeFunctionUrl and lambda:InvokeFunction (AWS change, Oct 2025) under
# distinct statement ids. compute_lambda and frontend are real modules; the
# providers are mocked, so this plans offline.
mock_provider "aws" {
  mock_data "aws_ec2_managed_prefix_list" {
    defaults = {
      id = "pl-3b927c52"
    }
  }

  mock_data "aws_availability_zones" {
    defaults = {
      names = ["us-east-1a", "us-east-1b", "us-east-1c"]
    }
  }

  # main.tf builds permissions_boundary_arn from this, and the provider
  # rejects an ARN whose account is not 12 digits. Synthetic placeholder.
  mock_data "aws_caller_identity" {
    defaults = {
      account_id = "123456789012"
    }
  }
}

mock_provider "random" {}
mock_provider "http" {}
mock_provider "external" {
  mock_data "external" {
    defaults = {
      result = { arch = "arm64" }
    }
  }
}

override_module {
  target = module.build
}

override_module {
  target = module.registry
}

# The real compute_lambda module validates these as concrete ARNs.
override_module {
  target = module.database
  outputs = {
    database_name        = "cudly"
    instance_address     = "db.example.com"
    proxy_endpoint       = "proxy.example.com"
    password_secret_name = "cudly-test-db-password"
    password_secret_arn  = "arn:aws:secretsmanager:us-east-1:123456789012:secret:cudly-test-db-password-AbCdEf"
    admin_email          = "admin@example.com"
  }
}

override_module {
  target = module.secrets
  outputs = {
    admin_password_secret_arn            = "arn:aws:secretsmanager:us-east-1:123456789012:secret:cudly-test-admin-AbCdEf"
    credential_encryption_key_secret_arn = "arn:aws:secretsmanager:us-east-1:123456789012:secret:cudly-test-cred-AbCdEf"
    scheduled_task_secret_arn            = "arn:aws:secretsmanager:us-east-1:123456789012:secret:cudly-test-sched-AbCdEf"
    scheduled_task_secret_name           = "cudly-test-sched"
  }
}

# The distribution ARN is unknown at plan time; pin it so source_arn is assertable.
# The real frontend module is covered by modules/frontend/aws/tests.
override_module {
  target = module.frontend
  outputs = {
    cloudfront_distribution_arn = "arn:aws:cloudfront::123456789012:distribution/E123EXAMPLE"
    cloudfront_distribution_id  = "E123EXAMPLE"
    cloudfront_domain_name      = "d111111abcdef8.cloudfront.net"
    frontend_url                = "https://d111111abcdef8.cloudfront.net"
  }
}

override_module {
  target = module.deployment_checks
}

variables {
  environment                 = "dev"
  region                      = "us-east-1"
  compute_platform            = "lambda"
  enable_cdn                  = true
  enable_docker_build         = false
  image_uri                   = "123456789012.dkr.ecr.us-east-1.amazonaws.com/cudly:test"
  admin_email                 = "admin@example.com"
  lambda_allowed_origins      = ["http://localhost:3000"]
  lambda_architecture         = "arm64"
  lambda_reserved_concurrency = 20
}

run "lambda_with_cdn_is_iam_gated_behind_oac" {
  command = plan

  assert {
    condition     = local.lambda_function_url_auth_type == "AWS_IAM"
    error_message = "enable_cdn = true must close the Function URL with AWS_IAM."
  }

  assert {
    condition     = length(module.frontend) == 1
    error_message = "The distribution must exist (frontend.tf enables its OAC for lambda + enable_cdn); the OAC itself is asserted in the frontend module test."
  }

  assert {
    condition     = length(aws_lambda_permission.function_url_cloudfront) == 1 && length(aws_lambda_permission.function_invoke_cloudfront) == 1
    error_message = "CloudFront needs both the InvokeFunctionUrl and the InvokeFunction permission."
  }

  assert {
    condition     = aws_lambda_permission.function_url_cloudfront[0].action == "lambda:InvokeFunctionUrl" && aws_lambda_permission.function_invoke_cloudfront[0].action == "lambda:InvokeFunction"
    error_message = "The two permissions must grant InvokeFunctionUrl and InvokeFunction respectively."
  }

  assert {
    condition     = aws_lambda_permission.function_url_cloudfront[0].statement_id != aws_lambda_permission.function_invoke_cloudfront[0].statement_id
    error_message = "Lambda rejects duplicate statement ids; the two permissions must use different ones."
  }

  assert {
    condition = alltrue([
      for p in [aws_lambda_permission.function_url_cloudfront[0], aws_lambda_permission.function_invoke_cloudfront[0]] :
      p.principal == "cloudfront.amazonaws.com" && p.source_arn == "arn:aws:cloudfront::123456789012:distribution/E123EXAMPLE"
    ])
    error_message = "Both permissions must be granted to the CloudFront service principal and scoped to the distribution ARN."
  }
}

run "trusted_proxy_hops_is_set_only_with_the_cdn" {
  command = plan

  assert {
    condition     = lookup(local.lambda_cdn_env, "CUDLY_TRUSTED_PROXY_HOPS", "") == "1"
    error_message = "With enable_cdn the Lambda must trust exactly one proxy hop (CloudFront) for X-Forwarded-For."
  }
}

run "no_trusted_proxy_hops_without_the_cdn" {
  command = plan

  variables {
    enable_cdn = false
  }

  assert {
    condition     = !contains(keys(local.lambda_cdn_env), "CUDLY_TRUSTED_PROXY_HOPS")
    error_message = "Without a CDN the Function URL is public and X-Forwarded-For is client-controlled; the hops variable must not be set."
  }
}
