package audit

import (
	"net"
	"testing"

	"github.com/google/uuid"
)

func TestTeachingAuditActionsRequireTheirOwnTargetType(t *testing.T) {
	base := Event{ActorUserID: uuid.New(), TargetID: uuid.NewString(), RequestID: "request-123", IP: net.ParseIP("192.0.2.1")}
	for _, tc := range []struct{ action, target string }{{"lesson.archived", "catalog"}, {"catalog.created", "lesson"}, {"student.created", "lesson"}} {
		event := base
		event.Action, event.TargetType = tc.action, tc.target
		if _, err := validateAndMarshal(event); err == nil {
			t.Fatalf("%s accepted %s target", tc.action, tc.target)
		}
	}
	base.Action, base.TargetType = "lesson.archived", "lesson"
	if _, err := validateAndMarshal(base); err != nil {
		t.Fatal(err)
	}
}

func TestFileLifecycleAuditActionsUseStableTargetsAndMetadata(t *testing.T) {
	base := Event{ActorUserID: uuid.New(), TargetID: uuid.NewString(), RequestID: "request-123", IP: net.ParseIP("192.0.2.2")}
	for _, tc := range []struct {
		action   string
		target   string
		metadata map[string]any
	}{
		{action: "file.uploaded", target: "file_version"},
		{action: "file.policy_changed", target: "lesson"},
		{action: "file.processing_retried", target: "file_version"},
		{action: "file.replaced", target: "file"},
		{action: "file.draft_rolled_back", target: "file_version"},
		{action: "file.delete_requested", target: "file"},
		{action: "file.cleanup_scheduled", target: "file_version", metadata: map[string]any{"previewCount": "2"}},
		{action: "file.cleanup_completed", target: "file_version"},
	} {
		event := base
		event.Action, event.TargetType, event.Metadata = tc.action, tc.target, tc.metadata
		if _, err := validateAndMarshal(event); err != nil {
			t.Fatalf("%s rejected: %v", tc.action, err)
		}
		event.TargetType = "lesson"
		if tc.target != "lesson" {
			if _, err := validateAndMarshal(event); err == nil {
				t.Fatalf("%s accepted lesson target", tc.action)
			}
		}
	}
}
