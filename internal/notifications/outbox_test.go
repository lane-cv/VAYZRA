package notifications

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDecodeLessonPublicationPayloadAcceptsVersionedAndLegacyRows(t *testing.T) {
	lesson, revision := uuid.New(), uuid.New()
	for _, raw := range []string{
		`{"schemaVersion":1,"lessonId":"` + lesson.String() + `","revisionId":"` + revision.String() + `"}`,
		`{"lesson_id":"` + lesson.String() + `","revision_id":"` + revision.String() + `"}`,
	} {
		got, err := decodeLessonPublicationPayload(json.RawMessage(raw))
		if err != nil || got.LessonID != lesson || got.RevisionID != revision {
			t.Fatalf("payload decoded as %#v, err=%v", got, err)
		}
	}
	for _, raw := range []string{
		`{}`, `{"schemaVersion":2,"lessonId":"` + lesson.String() + `","revisionId":"` + revision.String() + `"}`,
		`{"schemaVersion":1,"lessonId":"` + lesson.String() + `","revisionId":"` + revision.String() + `","studentId":"` + uuid.NewString() + `"}`,
	} {
		if _, err := decodeLessonPublicationPayload(json.RawMessage(raw)); !errors.Is(err, ErrPermanentOutbox) {
			t.Fatalf("payload %s err=%v, want permanent", raw, err)
		}
	}
}

func TestRetryDelayIsExponentialAndCapped(t *testing.T) {
	want := map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 10: 512 * time.Second, 11: 15 * time.Minute, 100: 15 * time.Minute}
	for attempts, expected := range want {
		if got := retryDelay(attempts); got != expected {
			t.Fatalf("attempts=%d delay=%s want=%s", attempts, got, expected)
		}
	}
}
