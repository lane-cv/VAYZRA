package teaching

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNormalizeExternalURLCanonicalizesAuthorityWithoutChangingResource(t *testing.T) {
	got, err := normalizeExternalURL("HTTPS://Video.Example.TEST:443/watch/Path?q=A#")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://video.example.test/watch/Path?q=A" {
		t.Fatalf("url=%q", got)
	}
}

func TestDraftValidationRejectsAllAudienceMembersAndUnsafeMarkdown(t *testing.T) {
	base := SaveDraftInput{LessonID: uuid.New(), ExpectedVersion: 1, Title: "Lesson", Audience: Audience{Mode: AudienceAll}}
	base.Audience.UserIDs = []uuid.UUID{uuid.New()}
	if validDraft(base) {
		t.Fatal("all audience accepted explicit users")
	}
	base.Audience.UserIDs = nil
	base.BodyMarkdown = `<script>alert(1)</script>`
	if validPersistedDraft(base) {
		t.Fatal("unsafe markdown accepted")
	}
	base.BodyMarkdown = strings.Repeat("```", 3)
	if validPersistedDraft(base) {
		t.Fatal("unbalanced fenced block accepted")
	}
}
