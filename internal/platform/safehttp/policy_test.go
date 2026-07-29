package safehttp

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

type fakeResolver struct {
	answers map[string][]netip.Addr
	err     error
	lookups []string
}

func (r *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.lookups = append(r.lookups, host)
	if r.err != nil {
		return nil, r.err
	}
	return r.answers[host], nil
}

type sequenceResolver struct {
	answers [][]netip.Addr
	lookups int
}

func (r *sequenceResolver) LookupNetIP(_ context.Context, _, _ string) ([]netip.Addr, error) {
	index := r.lookups
	r.lookups++
	if index >= len(r.answers) {
		index = len(r.answers) - 1
	}
	return r.answers[index], nil
}

type deadlineResolver struct {
	answer   netip.Addr
	deadline time.Time
	has      bool
}

func (r *deadlineResolver) LookupNetIP(ctx context.Context, _, _ string) ([]netip.Addr, error) {
	r.deadline, r.has = ctx.Deadline()
	return []netip.Addr{r.answer}, nil
}

func TestPolicyNormalizeBaseURL(t *testing.T) {
	t.Parallel()
	resolver := &fakeResolver{answers: map[string][]netip.Addr{
		"api.example.com": {netip.MustParseAddr("93.184.216.34")},
	}}
	policy := Policy{Resolver: resolver}

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{"accepts HTTPS", "https://api.example.com/v1", "https://api.example.com/v1", false},
		{"trims trailing slash", "https://api.example.com/v1/", "https://api.example.com/v1", false},
		{"rejects HTTP in production", "http://api.example.com", "", true},
		{"rejects credentials", "https://user:pass@api.example.com", "", true},
		{"rejects fragment", "https://api.example.com/#fragment", "", true},
		{"rejects empty fragment delimiter", "https://api.example.com/v1#", "", true},
		{"rejects query", "https://api.example.com/?key=value", "", true},
		{"rejects empty query delimiter", "https://api.example.com/v1?", "", true},
		{"rejects scheme relative", "//api.example.com/v1", "", true},
		{"rejects Unicode host", "https://\u0430pi.example.com", "", true},
		{"rejects IPv4-in-IPv6", "https://[::ffff:127.0.0.1]", "", true},
		{"rejects IP literals", "https://93.184.216.34/v1", "", true},
		{"rejects non-canonical HTTPS port", "https://api.example.com:444/v1", "", true},
		{"rejects empty port", "https://api.example.com:/v1", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := policy.NormalizeBaseURL(context.Background(), tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NormalizeBaseURL(%q) succeeded: %v", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPolicyValidateResolvedRejectsNonPublicAddresses(t *testing.T) {
	t.Parallel()
	for _, address := range []string{
		"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.0.1",
		"169.254.169.254", "100.64.0.1", "224.0.0.1", "0.0.0.0",
		"192.0.2.1", "198.51.100.1", "203.0.113.1", "::ffff:127.0.0.1",
		"0.1.2.3", "240.0.0.1", "64:ff9b::a00:1", "64:ff9b:1::a00:1",
		"2002:0a00:0001::1", "2001:0000::1", "192.88.99.1",
		"192.31.196.1", "192.52.193.1", "192.175.48.1", "2001:3::1",
		"2001:4:112::1", "2620:4f:8000::1", "3fff::1", "fe80::1",
		"ff02::1", "fec0::1",
	} {
		t.Run(address, func(t *testing.T) {
			policy := Policy{Resolver: &fakeResolver{answers: map[string][]netip.Addr{
				"blocked.test": {netip.MustParseAddr(address)},
			}}}
			if _, err := policy.ValidateResolved(context.Background(), "blocked.test"); err == nil {
				t.Fatal("expected non-public address to be rejected")
			}
		})
	}
}

func TestPolicyValidateResolvedRejectsSiteLocalIPv6DirectAndMixed(t *testing.T) {
	t.Parallel()
	policy := Policy{}
	if _, err := policy.ValidateResolved(context.Background(), "fec0::1"); err == nil {
		t.Fatal("direct site-local IPv6 address was accepted")
	}

	policy.Resolver = &fakeResolver{answers: map[string][]netip.Addr{
		"mixed.test": {
			netip.MustParseAddr("93.184.216.34"),
			netip.MustParseAddr("fec0::1"),
		},
	}}
	if _, err := policy.ValidateResolved(context.Background(), "mixed.test"); err == nil {
		t.Fatal("mixed public/site-local DNS answer was accepted")
	}
}

func TestPolicyNormalizeBaseURLBoundsResolverWhenCallerHasNoDeadline(t *testing.T) {
	t.Parallel()
	resolver := &deadlineResolver{answer: netip.MustParseAddr("93.184.216.34")}
	started := time.Now()
	if _, err := (Policy{Resolver: resolver}).NormalizeBaseURL(
		context.Background(),
		"https://api.example.com/v1",
	); err != nil {
		t.Fatal(err)
	}
	if !resolver.has {
		t.Fatal("resolver context had no deadline")
	}
	remaining := resolver.deadline.Sub(started)
	if remaining < 4*time.Second || remaining > 6*time.Second {
		t.Fatalf("resolver deadline remaining = %s, want about 5s", remaining)
	}
}

func TestPolicyRejectsIPv6Zone(t *testing.T) {
	t.Parallel()
	policy := Policy{Resolver: &fakeResolver{answers: map[string][]netip.Addr{}}}
	if _, err := policy.NormalizeBaseURL(context.Background(), "https://[fe80::1%25en0]/v1"); err == nil {
		t.Fatal("expected IPv6 zone identifier to be rejected")
	}
}

func TestPolicyValidateResolvedRejectsMixedDNSAnswer(t *testing.T) {
	t.Parallel()
	policy := Policy{Resolver: &fakeResolver{answers: map[string][]netip.Addr{
		"mixed.test": {
			netip.MustParseAddr("93.184.216.34"),
			netip.MustParseAddr("10.0.0.1"),
		},
	}}}
	if _, err := policy.ValidateResolved(context.Background(), "mixed.test"); err == nil {
		t.Fatal("expected mixed public/private DNS answer to be rejected")
	}
}

func TestPolicyDevelopmentPermitsOnlyLoopback(t *testing.T) {
	t.Parallel()
	resolver := &fakeResolver{answers: map[string][]netip.Addr{
		"localhost":    {netip.MustParseAddr("127.0.0.1")},
		"private.test": {netip.MustParseAddr("10.0.0.1")},
	}}
	policy := Policy{DevelopmentAllowPrivate: true, Resolver: resolver}
	if _, err := policy.ValidateResolved(context.Background(), "localhost"); err != nil {
		t.Fatalf("loopback rejected in development: %v", err)
	}
	if _, err := policy.ValidateResolved(context.Background(), "private.test"); err == nil {
		t.Fatal("private non-loopback address accepted in development")
	}
}

func TestPolicyRejectsLocalHostnamesAndResolverErrors(t *testing.T) {
	t.Parallel()
	policy := Policy{Resolver: &fakeResolver{err: errors.New("DNS unavailable")}}
	for _, host := range []string{"localhost", "printer.local"} {
		if _, err := policy.ValidateResolved(context.Background(), host); err == nil {
			t.Fatalf("%s was accepted", host)
		}
	}
	if _, err := policy.ValidateResolved(context.Background(), "api.example.com"); err == nil {
		t.Fatal("resolver failure accepted")
	}
}
