package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
)

// ScheduledEvent represents a generic scheduled event. Source, DetailType and
// Detail are the EventBridge envelope: ParseScheduledEvent never reads them
// (lambda.go routes on source separately), only Action and OwnerToken drive the
// task.
type ScheduledEvent struct {
	Source     string `json:"source"`
	DetailType string `json:"detail-type"`
	Action     string `json:"action"`
	// OwnerToken carries the collection-marker owner token (issue #261
	// compare-and-clear guard) for TaskCollectRecommendations events fired
	// by the async self-invoke in handler_recommendations_refresh.go.
	// EventBridge cron deliveries and the --task CLI never set this field,
	// so it decodes to "" and the scheduler treats the run as owning no
	// marker (see ScheduledTaskParams / clearCollectionStartedBestEffort).
	OwnerToken string          `json:"owner_token"`
	Detail     json.RawMessage `json:"detail"`
}

// ScheduledTaskParams carries per-task-type data extracted from a
// ScheduledEvent. Only TaskCollectRecommendations currently reads a field
// (OwnerToken); other task types receive a zero-value struct.
type ScheduledTaskParams struct {
	OwnerToken string
}

// ParseScheduledEvent parses a scheduled event and returns the task type
// plus any per-task parameters.
func ParseScheduledEvent(rawEvent json.RawMessage) (ScheduledTaskType, ScheduledTaskParams, error) {
	var event ScheduledEvent
	if err := json.Unmarshal(rawEvent, &event); err != nil {
		return "", ScheduledTaskParams{}, fmt.Errorf("failed to parse scheduled event: %w", err)
	}

	// Validate the owner token at the boundary rather than letting a malformed
	// value reach the UUID-typed persistence layer. Empty is legitimate and
	// expected (EventBridge cron, the /api/scheduled/ HTTP path, the --task
	// CLI): those callers never won MarkCollectionStarted and own no marker.
	// A non-empty value, though, can only have come from asyncInvokeSelf,
	// which always sends a uuid.New(), so anything else means the payload is
	// corrupt. Failing loud here is deliberate: a token that cannot match any
	// owner would leave the marker it belongs to stranded for the full
	// 5-minute recovery window anyway, so a buried error log on the eventual
	// clear is strictly worse than refusing the malformed event outright.
	//
	// uuid.Parse's error is deliberately NOT wrapped: for a 45-character input
	// it formats as "invalid urn prefix: %q" over the value's first nine bytes
	// (google/uuid uuid.go), so propagating it would echo part of the rejected
	// token into the error and from there into logs. The shape of the failure
	// carries no diagnostic value the fixed message below does not already
	// give, so the value is dropped rather than masked.
	if event.OwnerToken != "" {
		if _, err := uuid.Parse(event.OwnerToken); err != nil {
			return "", ScheduledTaskParams{}, errors.New("invalid owner_token in scheduled event: not a UUID")
		}
	}

	taskType, err := ParseScheduledTaskType(event.Action)
	if err != nil {
		return "", ScheduledTaskParams{}, err
	}
	return taskType, ScheduledTaskParams{OwnerToken: event.OwnerToken}, nil
}

// ParseScheduledTaskType maps a raw task name (an event action or the
// /api/scheduled/{task} path segment) to its ScheduledTaskType, rejecting
// names that are not in the roster.
func ParseScheduledTaskType(name string) (ScheduledTaskType, error) {
	if taskType, ok := scheduledEventActions[name]; ok {
		return taskType, nil
	}
	return "", fmt.Errorf("unknown scheduled task action: %q", name)
}

// ScheduledTaskNames returns every task name the server accepts from a
// scheduler, sorted. Infrastructure tests use it to check that each task is
// wired on every platform.
func ScheduledTaskNames() []string {
	names := make([]string, 0, len(scheduledEventActions))
	for name := range scheduledEventActions {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
