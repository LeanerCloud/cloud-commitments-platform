# With enable_oac the Lambda origin must be OAC-signed, and the cache behavior
# must forward x-amz-content-sha256 (needed for signed POST/PUT bodies) but not
# Authorization (CloudFront re-signs it). Providers are mocked, so this runs offline.
mock_provider "aws" {
  mock_resource "aws_cloudfront_function" {
    defaults = {
      arn = "arn:aws:cloudfront::123456789012:function/test"
    }
  }
}

variables {
  project_name       = "cudly"
  environment        = "dev"
  origin_domain_name = "abc123.lambda-url.us-east-1.on.aws"
  enable_oac         = true
}

run "oac_attached_and_sha256_forwarded" {
  # apply (mocked): the OAC id is unknown at plan time.
  command = apply

  assert {
    condition     = length(aws_cloudfront_origin_access_control.lambda) == 1
    error_message = "enable_oac = true must create the Lambda origin access control."
  }

  assert {
    condition     = alltrue([for o in aws_cloudfront_distribution.frontend.origin : o.origin_access_control_id == aws_cloudfront_origin_access_control.lambda[0].id])
    error_message = "Every origin must be signed with the Lambda origin access control."
  }

  assert {
    condition     = contains(tolist(aws_cloudfront_distribution.frontend.default_cache_behavior[0].forwarded_values[0].headers), "x-amz-content-sha256")
    error_message = "x-amz-content-sha256 must be forwarded to the Lambda origin."
  }

  assert {
    condition     = !contains(tolist(aws_cloudfront_distribution.frontend.default_cache_behavior[0].forwarded_values[0].headers), "Authorization")
    error_message = "Authorization must not be forwarded; OAC replaces it."
  }
}
