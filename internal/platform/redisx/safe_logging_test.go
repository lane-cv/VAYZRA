package redisx

import (
	"reflect"
	"testing"
)

func TestLimiterConstructorsUseInjectedDegradationCallbacks(t *testing.T) {
	tests := []struct {
		name    string
		trigger func(func(string))
	}{
		{
			name: "login",
			trigger: func(callback func(string)) {
				NewLoginLimiterWithLog(nil, Policy{}, callback).
					tripBreaker("allow")
			},
		},
		{
			name: "progress",
			trigger: func(callback func(string)) {
				NewProgressWriteLimiterWithLog(
					nil,
					ProgressLimitPolicy{},
					callback,
				).degraded("allow")
			},
		},
		{
			name: "search",
			trigger: func(callback func(string)) {
				NewSearchLimiterWithLog(
					nil,
					ResourceLimitPolicy{},
					callback,
				).degraded("search")
			},
		},
		{
			name: "provider test",
			trigger: func(callback func(string)) {
				limiter := NewProviderTestLimiterWithLog(
					nil,
					ResourceLimitPolicy{},
					callback,
				)
				limiter.(*SearchLimiter).degraded("provider_test")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var categories []string
			test.trigger(func(category string) {
				categories = append(categories, category)
			})
			if !reflect.DeepEqual(categories, []string{
				map[string]string{
					"login":         "allow",
					"progress":      "allow",
					"search":        "search",
					"provider test": "provider_test",
				}[test.name],
			}) {
				t.Fatalf("categories = %v", categories)
			}
		})
	}
}
