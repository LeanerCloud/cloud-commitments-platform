package config

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/stretchr/testify/assert"
)

func TestListDueLadderTranches_RejectsBadArgumentsBeforeTouchingTheDatabase(t *testing.T) {
	t.Parallel()
	store := &PostgresStore{}
	compute := []ladder.LayerType{ladder.LayerComputeSP}
	cases := []struct {
		name   string
		want   string
		layers []ladder.LayerType
		limit  int
	}{
		{name: "zero limit", limit: 0, layers: compute, want: "limit must be positive"},
		{name: "negative limit", limit: -1, layers: compute, want: "limit must be positive"},
		{name: "nil allowlist", limit: 1, layers: nil, want: "at least one allowed layer"},
		{name: "empty allowlist", limit: 1, layers: []ladder.LayerType{}, want: "at least one allowed layer"},
		{name: "unknown layer", limit: 1, layers: []ladder.LayerType{"nope"}, want: "layer allowlist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := store.ListDueLadderTranches(context.Background(), "cfg", time.Now(), tc.limit, tc.layers)
			assert.Nil(t, rows)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}
