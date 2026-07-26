package aiqa

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSummaryCursorCanonicalRoundTripIncludesChannel(t *testing.T) {
	cursor := SummaryCursor{
		LastMessageAt: time.Date(2026, 7, 27, 2, 3, 4, 5, time.UTC),
		Channel:       "teacher",
		ID:            uuid.MustParse("10000000-0000-4000-8000-000000000001"),
	}
	encoded := encodeSummaryCursor(cursor)
	decoded, err := decodeSummaryCursor(encoded, time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if decoded != cursor {
		t.Fatalf("decoded=%+v want=%+v", decoded, cursor)
	}
	if _, err := decodeSummaryCursor(encoded+"=", time.Now().UTC()); err == nil {
		t.Fatal("non-canonical cursor accepted")
	}
}

func TestSummaryFilterRejectsInvalidChannelAndLimit(t *testing.T) {
	for _, filter := range []SummaryFilter{
		{Channel: "staff", Limit: 20},
		{Channel: "ai", Limit: 0},
		{Channel: "teacher", Limit: 101},
	} {
		if err := validateSummaryFilter(filter); err == nil {
			t.Fatalf("accepted %+v", filter)
		}
	}
}
