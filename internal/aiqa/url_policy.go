package aiqa

import (
	"context"
	"net/netip"
	"net/url"

	"happylearn.local/app/internal/platform/safehttp"
)

// URLPolicy preserves the Phase 4 provider policy API while delegating its
// network classification to the shared platform implementation.
type URLPolicy struct {
	DevelopmentAllowPrivate bool
	Resolver                interface {
		LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
	}
}

// NormalizeBaseURL validates a provider endpoint and removes one trailing slash.
func (p URLPolicy) NormalizeBaseURL(ctx context.Context, raw string) (*url.URL, error) {
	return p.safeHTTPPolicy().NormalizeBaseURL(ctx, raw)
}

// ValidateResolved resolves host and rejects any forbidden answer.
func (p URLPolicy) ValidateResolved(ctx context.Context, host string) ([]netip.Addr, error) {
	return p.safeHTTPPolicy().ValidateResolved(ctx, host)
}

func (p URLPolicy) validateRequestURL(ctx context.Context, u *url.URL) ([]netip.Addr, error) {
	return p.safeHTTPPolicy().ValidateRequestURL(ctx, u)
}

func (p URLPolicy) safeHTTPPolicy() safehttp.Policy {
	return safehttp.Policy{
		DevelopmentAllowPrivate: p.DevelopmentAllowPrivate,
		Resolver:                p.Resolver,
	}
}
