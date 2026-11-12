package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The listing request sent to AWS must carry each tier's term and price, in
// order. The attempt tests compare against toAWSPriceSchedule itself, so a swapped
// or dropped field in the conversion would pass them.
func TestToAWSPriceSchedule_CarriesTermAndPriceInOrder(t *testing.T) {
	got := toAWSPriceSchedule([]MarketplacePriceTier{
		{TermMonths: 12, Price: 99.5},
		{TermMonths: 6, Price: 51.25},
		{TermMonths: 1, Price: 9},
	})

	want := []ec2MarketplacePriceTier{
		{Term: 12, Price: 99.5},
		{Term: 6, Price: 51.25},
		{Term: 1, Price: 9},
	}
	assert.Equal(t, want, got)
}

func TestToAWSPriceSchedule_EmptyScheduleIsEmptyNotNil(t *testing.T) {
	got := toAWSPriceSchedule(nil)

	assert.NotNil(t, got)
	assert.Empty(t, got)
}
