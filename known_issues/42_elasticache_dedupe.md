# Known Issues: ElastiCache reserved-node duplicate protection

> **Audit status (2026-11-09):** `2 needs triage · 2 resolved`

## RESOLVED: a recent same-pool ElastiCache reservation could be bought again (#1863)

**Files:** `internal/purchase/elasticache_recent_check.go`

**Description:** Before each AWS ElastiCache purchase the platform lists the live reservations and refuses the rec when a recent reservation of the same region, node type and a compatible engine already covers it (a Redis reservation can also cover Valkey). Reservations created by the same execution are filtered out so a retry adopts its own purchase. This catches duplicates across executions only.

**Status:** Resolved (#1863).

## RESOLVED: two same-pool recs in one execution both bought (#1867)

**Files:** `internal/purchase/elasticache_duplicate_pool.go`, `internal/purchase/manager.go` (`preExecutionRefusal`)

**Description:** The per-rec checks run concurrently and drop the execution's own reservation ids, so neither saw the other and both bought. `hasDuplicateElastiCachePool` now refuses the whole selection before submission with a fixed, allowlisted text (`elastiCacheDuplicatePoolRefusal`). The key is account, region, node type (raw) and engine (trimmed, normalized); term, payment and count are not in it, so two same-pool recs with different terms are refused too. Only selected AWS ElastiCache recs count. Cross-execution duplicates stay the #1863 check's job.

**Status:** Resolved (#1867).

## LOW: Redis and Valkey of the same node type in one execution are both allowed

**Description:** They are distinct pools, so the same-pool refusal lets both through, although a Redis reservation also covers Valkey. The result can be redundant spend. Refusing it would be an owner decision.

**Status:** Needs triage.

## LOW: a refused plan step is regenerated and refused every tick

**Description:** A plan-step root with a duplicate pair ends failed with `no_submission` and releases its suppressions. If the plan regenerates the same duplicate recs, each tick yields a new refused row. No money moves. The Redshift same-tuple refusal behaves the same way since #1895.

**Status:** Needs triage.
