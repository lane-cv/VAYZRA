package queuepolicy

import "testing"

func TestOutboxMaxAttemptsMatchesProductionPolicy(t *testing.T) {
	if OutboxMaxAttempts != 10 {
		t.Fatalf("OutboxMaxAttempts=%d want=10", OutboxMaxAttempts)
	}
}
