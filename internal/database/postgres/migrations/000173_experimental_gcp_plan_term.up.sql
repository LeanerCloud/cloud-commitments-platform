-- 000173: add experimental_gcp_plan_term to global_config (platform issue #858)
--
-- When enabled, GCP Compute recommendation collection sets
-- RecommendationParams.ExperimentalGCPPlanTerm so the CUD term comes from each
-- recommendation's root Commitment plan. EXPERIMENTAL: the plan payload shape
-- is unverified against a live Recommender.
--
-- Default is false so existing deployments are unaffected on upgrade.
ALTER TABLE global_config
  ADD COLUMN IF NOT EXISTS experimental_gcp_plan_term BOOLEAN NOT NULL DEFAULT false;
