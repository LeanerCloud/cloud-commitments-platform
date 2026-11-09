package api

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// riExchangeDirectResolveRequest is the body of POST
// /api/ri-exchange/direct-executions/{id}/resolve. expected_status is the
// status the operator saw in the list; outcome "failed" releases the source
// RIs and needs acknowledge_unknown_outcome.
type riExchangeDirectResolveRequest struct {
	Outcome        string                             `json:"outcome"`
	ExpectedStatus string                             `json:"expected_status"`
	Reason         string                             `json:"reason"`
	Acknowledge    bool                               `json:"acknowledge_unknown_outcome"`
	CloudCheck     *riExchangeDirectResolveCloudCheck `json:"cloud_check"`
}

type riExchangeDirectResolveCloudCheck struct {
	Region    string `json:"checked_region"`
	CheckedAt string `json:"checked_at"`
}

// decodeRIExchangeDirectResolve reads one JSON document and checks its shape
// against the row being resolved. Which times the store admits is decided
// there, on the database clock.
func decodeRIExchangeDirectResolve(raw, rowRegion string) (riExchangeDirectResolveRequest, time.Time, error) {
	var body riExchangeDirectResolveRequest
	if len(raw) > riExchangeResolveBodyMax {
		return body, time.Time{}, NewClientError(400, "RI exchange resolve body is too large")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return body, time.Time{}, NewClientError(400, "Invalid RI exchange resolve body")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return body, time.Time{}, NewClientError(400, "Expected one RI exchange resolve body")
	}
	body.Reason = strings.TrimSpace(body.Reason)
	if err := body.validate(); err != nil {
		return body, time.Time{}, err
	}
	at, err := body.checkedAt(rowRegion)
	return body, at, err
}

func (b riExchangeDirectResolveRequest) validate() error {
	switch {
	case b.Outcome != config.RIExchangeDirectStatusFailed && b.Outcome != config.RIExchangeDirectStatusCompleted:
		return NewClientError(400, `outcome must be "failed" or "completed"`)
	case b.ExpectedStatus != config.RIExchangeDirectStatusSubmitted && b.ExpectedStatus != config.RIExchangeDirectStatusUnknown:
		return NewClientError(400, `expected_status must be "submitted" or "unknown"`)
	case utf8.RuneCountInString(b.Reason) < riExchangeResolveReasonMin || utf8.RuneCountInString(b.Reason) > riExchangeResolveReasonMax:
		return NewClientError(400, fmt.Sprintf("reason must be %d to %d characters", riExchangeResolveReasonMin, riExchangeResolveReasonMax))
	case hasControlRune(b.Reason):
		return NewClientError(400, "reason must not contain control characters")
	}
	return b.validateCheckAndAck()
}

func (b riExchangeDirectResolveRequest) validateCheckAndAck() error {
	switch {
	case b.CloudCheck == nil:
		return NewClientError(400, "cloud_check is required: say where you verified the exchange at AWS")
	case b.Outcome == config.RIExchangeDirectStatusFailed && !b.Acknowledge:
		return NewClientError(400, "acknowledge_unknown_outcome is required to release the source RIs: CUDly cannot see whether the exchange happened")
	}
	return nil
}

// checkedAt validates the shape of the operator's AWS-side check: the region
// it names must be the row's own.
func (b riExchangeDirectResolveRequest) checkedAt(rowRegion string) (time.Time, error) {
	c := b.CloudCheck
	if !riExchangeResolveRegionRe.MatchString(c.Region) || c.Region != rowRegion {
		return time.Time{}, NewClientError(400, "checked_region must be the region of the exchange")
	}
	at, err := time.Parse(time.RFC3339, c.CheckedAt)
	if err != nil {
		return time.Time{}, NewClientError(400, "checked_at must be an RFC 3339 timestamp")
	}
	return at.UTC(), nil
}

// note is the exact text stored in resolution_note. The fields are in a fixed
// order and quoted, so a repeat of the same request produces the same string
// and a different one cannot.
func (b riExchangeDirectResolveRequest) note(checkedAt time.Time) string {
	s := fmt.Sprintf("operator:%s: region=%q checked_at=%s", b.Outcome, b.CloudCheck.Region, checkedAt.Format(time.RFC3339Nano))
	if b.Outcome == config.RIExchangeDirectStatusFailed {
		s += " ack=true"
	}
	return s + fmt.Sprintf(" reason=%q", b.Reason)
}
