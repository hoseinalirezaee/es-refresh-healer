package patcher

import (
	"encoding/json"
	"testing"
)

func TestBuildPatch(t *testing.T) {
	patch, err := BuildPatch(12345, ReasonStaleRefresh)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var decoded map[string]map[string]map[string]string
	if err := json.Unmarshal(patch, &decoded); err != nil {
		t.Fatalf("patch is not valid JSON: %v", err)
	}

	annotations := decoded["metadata"]["annotations"]
	if annotations[LastKickAnnotation] != "12345" {
		t.Fatalf("expected last kick annotation, got %q", annotations[LastKickAnnotation])
	}
	if annotations[LastReasonAnnotation] != ReasonStaleRefresh {
		t.Fatalf("expected last reason annotation, got %q", annotations[LastReasonAnnotation])
	}
}
