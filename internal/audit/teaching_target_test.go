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
