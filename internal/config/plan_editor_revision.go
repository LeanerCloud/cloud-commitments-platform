package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// Revision fingerprints captured current state; callers must still recheck authority under locks.
func (s *PlanEditorSnapshot) Revision() (string, error) {
	if s == nil || s.Plan == nil {
		return "", errors.New("plan editor snapshot and plan are required")
	}
	state := struct {
		Domain             string
		Snapshot           *PlanEditorSnapshot
		ExecutionVersions  []planEditorExecutionVersion
		Services           json.RawMessage
		Ramp               json.RawMessage
		IncomingRetryRoots []string
	}{"plan-editor-state-v2", s, s.executionVersions, s.servicesJSON, s.rampScheduleJSON, s.incomingRetryRootIDs}
	raw, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("marshal plan editor state: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var canonical any
	if decodeErr := decoder.Decode(&canonical); decodeErr != nil {
		return "", fmt.Errorf("decode plan editor state: %w", decodeErr)
	}
	raw, err = json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal canonical plan editor state: %w", err)
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
