package config

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/stretchr/testify/assert"
)

func TestListDueLadderTranches_RejectsBadArgumentsBeforeTouchingTheDatabase(t *testing.T) {
	store := &PostgresStore{}
	compute := []ladder.LayerType{ladder.LayerComputeSP}
	cases := []struct {
		name   string
		limit  int
		layers []ladder.LayerType
		want   string
	}{
		{"zero limit", 0, compute, "limit must be positive"},
		{"negative limit", -1, compute, "limit must be positive"},
		{"nil allowlist", 1, nil, "at least one allowed layer"},
		{"empty allowlist", 1, []ladder.LayerType{}, "at least one allowed layer"},
		{"unknown layer", 1, []ladder.LayerType{"nope"}, "layer allowlist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := store.ListDueLadderTranches(context.Background(), "cfg", time.Now(), tc.limit, tc.layers)
			assert.Nil(t, rows)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}
