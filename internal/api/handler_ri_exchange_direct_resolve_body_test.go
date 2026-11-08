package api

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// Body validation for the direct-row resolve. Every case is a 400 that never
// reaches the store.
func TestResolveRIExchangeDirect_BodyValidation(t *testing.T) {
	check := func(region, at string) string {
		return fmt.Sprintf(`"cloud_check":{"checked_region":%q,"checked_at":%q}`, region, at)
	}
	good := check(drRegion, "2026-10-19T08:00:00Z")
	reason := `"reason":"source RIs still active in the EC2 console"`
	head := `{"outcome":"failed","expected_status":"unknown",` + reason + `,"acknowledge_unknown_outcome":true,`
	for name, body := range map[string]string{
		"empty":                                    ``,
		"unknown field":                            head + good + `,"exchange_id":"x"}`,
		"two documents":                            drFailedBody + drFailedBody,
		"oversized body":                           `{"outcome":"completed","reason":"` + strings.Repeat("x", 5000) + `"}`,
		"bad outcome":                              `{"outcome":"canceled","expected_status":"unknown",` + reason + `,` + good + `}`,
		"missing outcome":                          `{"expected_status":"unknown",` + reason + `,` + good + `}`,
		"missing expected status":                  `{"outcome":"completed",` + reason + `,` + good + `}`,
		"bad expected status":                      `{"outcome":"completed","expected_status":"completed",` + reason + `,` + good + `}`,
		"short reason (9 runes)":                   `{"outcome":"completed","expected_status":"unknown","reason":"123456789",` + good + `}`,
		"whitespace reason":                        `{"outcome":"completed","expected_status":"unknown","reason":"            ",` + good + `}`,
		"long reason (301 runes)":                  `{"outcome":"completed","expected_status":"unknown","reason":"` + strings.Repeat("é", 301) + `",` + good + `}`,
		"300 emoji overflow the note's byte limit": `{"outcome":"completed","expected_status":"unknown","reason":"` + strings.Repeat("😀", 300) + `",` + good + `}`,
		"control char in reason":                   `{"outcome":"completed","expected_status":"unknown","reason":"source RIs\nstill active",` + good + `}`,
		"missing cloud check":                      `{"outcome":"completed","expected_status":"unknown",` + reason + `}`,
		"cloud check null":                         head + `"cloud_check":null}`,
		"failed without ack":                       `{"outcome":"failed","expected_status":"unknown",` + reason + `,` + good + `}`,
		"failed with ack false":                    `{"outcome":"failed","expected_status":"unknown",` + reason + `,"acknowledge_unknown_outcome":false,` + good + `}`,
		"wrong region":                             head + check("us-east-1", "2026-10-19T08:00:00Z") + `}`,
		"malformed region":                         head + check("EU WEST", "2026-10-19T08:00:00Z") + `}`,
		"empty region":                             head + check("", "2026-10-19T08:00:00Z") + `}`,
		"bad checked_at":                           head + check(drRegion, "yesterday") + `}`,
		"missing checked_at":                       head + `"cloud_check":{"checked_region":"eu-west-1"}}`,
		"account in the cloud check":               head + `"cloud_check":{"checked_region":"eu-west-1","checked_account_id":"123","checked_at":"2026-10-19T08:00:00Z"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			store := drStore()
			_, err := drCaller{perms: riExecute}.call(store, dlID1, body)
			assert.Equal(t, 400, clientCode(t, err))
			assert.Zero(t, store.called, "an invalid body must not reach the store")
		})
	}
}

// Reasons at the rune boundaries are accepted, and the stored note quotes them.
func TestResolveRIExchangeDirect_ReasonBoundaries(t *testing.T) {
	for name, reason := range map[string]string{
		"10 runes":                strings.Repeat("é", 10),
		"300 runes":               strings.Repeat("é", 300),
		"trimmed to 10 runes":     "  " + strings.Repeat("a", 10) + "  ",
		"a quote is escaped once": `say "ok" to AWS`,
	} {
		t.Run(name, func(t *testing.T) {
			store := drStore()
			body := fmt.Sprintf(`{"outcome":"completed","expected_status":"unknown","reason":%q,"cloud_check":{"checked_region":%q,"checked_at":"2026-10-19T08:00:00Z"}}`, reason, drRegion)
			_, err := drCaller{perms: riExecute}.call(store, dlID1, body)
			require.NoError(t, err)
			assert.Contains(t, store.got.Note, fmt.Sprintf("reason=%q", strings.TrimSpace(reason)))
		})
	}
}

// completed does not need the acknowledgement; failed with it passes, and the
// note records it.
func TestResolveRIExchangeDirect_AckOnlyForFailed(t *testing.T) {
	store := drStore()
	_, err := drCaller{perms: riExecute}.call(store, dlID1, drBody("completed", ""))
	require.NoError(t, err)
	assert.NotContains(t, store.got.Note, "ack=true")
	assert.Equal(t, config.RIExchangeDirectStatusCompleted, store.got.Outcome)
}
