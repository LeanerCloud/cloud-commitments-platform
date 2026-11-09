package purchase

// #1844 S2: a credential or pin failure happens before any purchase call, so
// its account row records no_submission (releasing it from the ramp) and counts
// as proof for the root, unless the row was lost or another account may have
// submitted.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecuteMultiAccount_CredentialAndPinRows(t *testing.T) {
	bothRefused := map[string]recBehavior{skipAcctA: recRefused, skipAcctB: recRefused}
	cases := map[string]struct {
		fx        fanOutFixture
		wantProof bool
		wantRowA  bool // account A's row exists
		flaggedA  bool
	}{
		"a credential failure row is flagged and the root proven": {
			fx:        fanOutFixture{behaviors: bothRefused, credFail: map[string]bool{skipAcctA: true, skipAcctB: true}},
			wantProof: true, wantRowA: true, flaggedA: true,
		},
		"a pin failure row is flagged and the root proven": {
			fx:        fanOutFixture{behaviors: bothRefused, pinFail: true},
			wantProof: true, wantRowA: true, flaggedA: true,
		},
		"a credential failure next to a started plain failure stays unproven": {
			fx: fanOutFixture{
				behaviors: map[string]recBehavior{skipAcctA: recRefused, skipAcctB: recPlainFail},
				credFail:  map[string]bool{skipAcctA: true},
			},
			wantRowA: true, flaggedA: true,
		},
		"a lost credential row stays unproven and unflagged": {
			fx:        fanOutFixture{behaviors: bothRefused, credFail: map[string]bool{skipAcctA: true}, saveFail: map[string]bool{skipAcctA: true}},
			wantProof: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := tc.fx.runRoot(t)
			require.ErrorIs(t, r.err, errAllAccountsFailed)
			row := savedAccountRow(r.saved, skipAcctA)
			if !tc.wantRowA {
				assert.Nil(t, row)
			} else {
				require.NotNil(t, row)
				assert.Equal(t, "failed", row.Status)
				assert.Equal(t, tc.flaggedA, row.NoSubmission)
			}
			r.saveTerminal(t)
			assert.Equal(t, tc.wantProof, r.root.NoSubmission, "root no_submission")
		})
	}
}
