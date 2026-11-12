package purchase

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The two #1050 RI exchange notes are stored as the record's error and read
// back through SanitizeStoredError on History, so they must pass verbatim,
// and anything that is not their closed shape must still fall back.
const (
	riExchangeSourceInFlightNote = "RI exchange source RI is held by an exchange in progress: source RI(s) ri-0a1b2c3d, 1ba8e2e3-0c4f-4a7e-9a7b-5d6e7f8a9b0c already have an exchange in progress; resolve that exchange first"
	riExchangeOutcomeUnknownHead = "outcome unknown: "
	riExchangeOutcomeUnknownTail = "; verify the source RIs and target reservation at AWS, then POST /api/ri-exchange/resolve/550e8400-e29b-41d4-a716-000000010501"
)

func TestSanitizeStoredError_RIExchangeNotesPassVerbatim(t *testing.T) {
	reasons := []string{
		ErrCategoryMessage(ErrCategoryUnknown),
		ErrCategoryMessage(ErrCategoryAccessDenied),
		ErrCategoryMessage(ErrCategoryUnavailable),
		ErrCategoryMessage(ErrCategoryLimit),
		outcomeUnconfirmedMessage,
	}
	notes := make([]string, 0, len(reasons)+1)
	notes = append(notes, riExchangeSourceInFlightNote)
	for _, reason := range reasons {
		notes = append(notes, riExchangeOutcomeUnknownHead+reason+riExchangeOutcomeUnknownTail)
	}
	// The resolve route accepts an upper-case id, so the note may carry one.
	notes = append(notes, riExchangeOutcomeUnknownHead+outcomeUnconfirmedMessage+riExchangeOutcomeUnknownTail[:len(riExchangeOutcomeUnknownTail)-36]+strings.ToUpper(riExchangeOutcomeUnknownTail[len(riExchangeOutcomeUnknownTail)-36:]))
	notes = append(notes, sourceInFlightNote(strings.Repeat("a", 64)))
	for _, note := range notes {
		assert.Equal(t, note, SanitizeStoredError(note))
	}
}

func TestSanitizeStoredError_RIExchangeNotesStayClosed(t *testing.T) {
	leak := "arn:aws:ec2:us-east-1:123456789012:reserved-instances/ri-leak"
	for _, in := range []string{
		// A raw provider reason in place of a summarized one.
		riExchangeOutcomeUnknownHead + "RequestError: send request failed for " + leak + riExchangeOutcomeUnknownTail,
		// A resolve id that is not a UUID.
		riExchangeOutcomeUnknownHead + ErrCategoryMessage(ErrCategoryUnknown) + "; verify the source RIs and target reservation at AWS, then POST /api/ri-exchange/resolve/" + leak,
		// RI ids carrying text the guard never writes.
		"RI exchange source RI is held by an exchange in progress: source RI(s) ri-1 " + leak + " already have an exchange in progress; resolve that exchange first",
		// Text ahead of either note (the patterns are anchored at the head).
		leak + " " + riExchangeSourceInFlightNote,
		leak + ": " + riExchangeOutcomeUnknownHead + outcomeUnconfirmedMessage + riExchangeOutcomeUnknownTail,
		// An RI id past 64 characters, and an empty RI list.
		sourceInFlightNote(strings.Repeat("a", 65)),
		sourceInFlightNote(""),
	} {
		out := SanitizeStoredError(in)
		assert.NotEqual(t, in, out, in)
		assert.NotContains(t, out, leak)
	}
}

func sourceInFlightNote(ids string) string {
	return "RI exchange source RI is held by an exchange in progress: source RI(s) " + ids + " already have an exchange in progress; resolve that exchange first"
}

func TestSanitizeStoredError_QuotedValueSlotsAreClosed(t *testing.T) {
	slots := []struct {
		name, tmpl string
		ok, bad    []string
	}{
		{"reserved ceiling", `reserved ceiling "%s" is not a decimal`, []string{"0", "12.5", "800.000000", "999999999.12345678"}, nil},
		{"invalid ceiling", `exchange reservation returned invalid ceiling "%s"`, []string{"0", "12.5", "800.000000"}, nil},
		{"provider", `unknown provider "%s" (valid: aws, azure, gcp)`, []string{"AWS", "Azure", "GCP", "digitalocean"}, nil},
	}
	common := []string{"10.0.0.1", "123456789012", "db.internal.example.com", "12.5.1", "a b"}
	for _, sl := range slots {
		for _, v := range sl.ok {
			note := fmt.Sprintf(sl.tmpl, v)
			assert.Equal(t, note, SanitizeStoredError(note), note)
		}
		for _, v := range common {
			note := fmt.Sprintf(sl.tmpl, v)
			out := SanitizeStoredError(note)
			assert.NotEqual(t, note, out, note)
			assert.NotContains(t, out, v, note)
		}
	}
}
