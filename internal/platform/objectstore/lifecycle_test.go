package objectstore

import (
	"context"
	"errors"
	"testing"

	"github.com/minio/minio-go/v7/pkg/lifecycle"
)

func TestIncompleteMultipartLifecycleIsCanonicalAndIdempotent(t *testing.T) {
	canonical := lifecycle.Rule{
		ID:                             incompleteMultipartLifecycleRuleID,
		Status:                         "Enabled",
		AbortIncompleteMultipartUpload: lifecycle.AbortIncompleteMultipartUpload{DaysAfterInitiation: 2},
	}
	unrelated := lifecycle.Rule{ID: "keep-existing-rule", Status: "Enabled", Expiration: lifecycle.Expiration{Days: 30}}

	t.Run("installs and preserves unrelated rule", func(t *testing.T) {
		client := &fakeLifecycleClient{config: &lifecycle.Configuration{Rules: []lifecycle.Rule{unrelated}}}
		if err := ensureIncompleteMultipartLifecycle(context.Background(), client, "private-originals"); err != nil {
			t.Fatal(err)
		}
		if client.setCalls != 1 || !hasIncompleteMultipartLifecycle(client.config, 2) || !containsLifecycleRule(client.config, "keep-existing-rule") {
			t.Fatalf("sets=%d config=%+v", client.setCalls, client.config)
		}
	})

	variants := map[string][]lifecycle.Rule{
		"restrictive filter": {{
			ID:                             incompleteMultipartLifecycleRuleID,
			Status:                         "Enabled",
			RuleFilter:                     lifecycle.Filter{Prefix: "restricted/"},
			AbortIncompleteMultipartUpload: lifecycle.AbortIncompleteMultipartUpload{DaysAfterInitiation: 2},
		}},
		"extra deletion actions": {{
			ID:                             incompleteMultipartLifecycleRuleID,
			Status:                         "Enabled",
			AbortIncompleteMultipartUpload: lifecycle.AbortIncompleteMultipartUpload{DaysAfterInitiation: 2},
			Expiration:                     lifecycle.Expiration{Days: 1},
			Transition:                     lifecycle.Transition{Days: 1, StorageClass: "GLACIER"},
		}},
		"duplicate reserved rules": {
			canonical,
			{ID: incompleteMultipartLifecycleRuleID, Status: "Enabled", RuleFilter: lifecycle.Filter{Prefix: "restricted/"}, AbortIncompleteMultipartUpload: lifecycle.AbortIncompleteMultipartUpload{DaysAfterInitiation: 2}},
		},
	}
	for name, reserved := range variants {
		t.Run(name, func(t *testing.T) {
			client := &fakeLifecycleClient{config: &lifecycle.Configuration{Rules: append([]lifecycle.Rule{unrelated}, reserved...)}}
			if err := ensureIncompleteMultipartLifecycle(context.Background(), client, "private-originals"); err != nil {
				t.Fatal(err)
			}
			if client.setCalls != 1 || !hasIncompleteMultipartLifecycle(client.config, 2) || countLifecycleRules(client.config, incompleteMultipartLifecycleRuleID) != 1 || !containsLifecycleRule(client.config, "keep-existing-rule") {
				t.Fatalf("sets=%d config=%+v", client.setCalls, client.config)
			}
		})
	}

	t.Run("preserves canonical rule idempotently", func(t *testing.T) {
		client := &fakeLifecycleClient{config: &lifecycle.Configuration{Rules: []lifecycle.Rule{unrelated, canonical}}}
		if err := ensureIncompleteMultipartLifecycle(context.Background(), client, "private-originals"); err != nil {
			t.Fatal(err)
		}
		if client.setCalls != 0 || !hasIncompleteMultipartLifecycle(client.config, 2) {
			t.Fatalf("sets=%d config=%+v", client.setCalls, client.config)
		}
	})

	unverified := &fakeLifecycleClient{ignoreSet: true}
	if err := ensureIncompleteMultipartLifecycle(context.Background(), unverified, "private-originals"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unverified lifecycle err=%v", err)
	}
}

func countLifecycleRules(config *lifecycle.Configuration, id string) int {
	count := 0
	for _, rule := range config.Rules {
		if rule.ID == id {
			count++
		}
	}
	return count
}

func containsLifecycleRule(config *lifecycle.Configuration, id string) bool {
	return countLifecycleRules(config, id) > 0
}

type fakeLifecycleClient struct {
	config    *lifecycle.Configuration
	setCalls  int
	ignoreSet bool
}

func (f *fakeLifecycleClient) GetBucketLifecycle(context.Context, string) (*lifecycle.Configuration, error) {
	if f.config == nil {
		return &lifecycle.Configuration{}, nil
	}
	return f.config, nil
}
func (f *fakeLifecycleClient) SetBucketLifecycle(_ context.Context, _ string, config *lifecycle.Configuration) error {
	f.setCalls++
	if !f.ignoreSet {
		copyConfig := *config
		copyConfig.Rules = append([]lifecycle.Rule(nil), config.Rules...)
		f.config = &copyConfig
	}
	return nil
}
