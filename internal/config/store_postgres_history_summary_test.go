package config

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestExecutionSummaryQuery_DateBoundExemptsStatuses(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 7)
	exempt := []string{"pending", "notified"}

	query, args := executionSummaryQuery(HistorySummaryFilter{
		ExecutionStatuses:           []string{"pending", "failed"},
		ExecutionStart:              &start,
		ExecutionEnd:                &end,
		ExecutionDateExemptStatuses: exempt,
	})

	assert.Equal(t, []any{[]string{"pending", "failed"}, exempt, start, end}, args)
	assert.Contains(t, query, "(status = ANY($2) OR scheduled_date >= $3)")
	assert.Contains(t, query, "(status = ANY($2) OR scheduled_date <= $4)")
}

func TestExecutionSummaryQuery_NoExemptionKeepsPlainBound(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	query, args := executionSummaryQuery(HistorySummaryFilter{ExecutionStatuses: []string{"failed"}, ExecutionStart: &start})

	assert.Equal(t, []any{[]string{"failed"}, start}, args)
	assert.Contains(t, query, "AND scheduled_date >= $2")
	assert.NotContains(t, query, "status = ANY($2)")
}

// The legacy-estimate FILTER (issue #669) sits on both SUMs and on both sides
// of the unknown-upfront comparison, never in a WHERE, and is absent on a
// schema without the column.
func TestPurchaseHistorySummaryQuery_LegacyEstimateFilter(t *testing.T) {
	filter := " FILTER (WHERE amount_basis IS DISTINCT FROM 'legacy_window')"
	with := fmt.Sprintf(purchaseHistorySummaryQuery, "", legacyEstimateFilter)
	assert.Contains(t, with, "COUNT(upfront_cost)"+filter+" = COUNT(*)"+filter)
	assert.Contains(t, with, "SUM(upfront_cost)"+filter)
	assert.Contains(t, with, "SUM(estimated_savings)"+filter)
	assert.Contains(t, with, "COUNT(*),", "the purchase count is unfiltered")
	assert.NotContains(t, with, "purchase_history WHERE", "the flag is never a WHERE")

	without := fmt.Sprintf(purchaseHistorySummaryQuery, "", "")
	assert.NotContains(t, without, "FILTER")
	assert.Contains(t, without, "COUNT(upfront_cost) = COUNT(*)")
}
