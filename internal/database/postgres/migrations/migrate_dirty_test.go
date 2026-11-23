package migrations

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// dirtyCheckEnabled is default-on: only an explicit ParseBool falsey value
// disables it, so a typo or an empty value must not silently drop the guard.
func TestDirtyCheckEnabled(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want bool
	}{
		{"empty keeps the check on", "", true},
		{"unparseable keeps the check on", "maybe", true},
		{"true", "true", true},
		{"1", "1", true},
		{"false disables", "false", false},
		{"0 disables", "0", false},
		{"f disables", "f", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CUDLY_MIGRATION_DIRTY_CHECK", tt.env)
			assert.Equal(t, tt.want, dirtyCheckEnabled())
		})
	}
}
