package aiqa

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// URLPolicy limits AI provider egress to publicly routable HTTPS endpoints.
type URLPolicy struct {
	DevelopmentAllowPrivate bool
	Resolver                interface {
		LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
	}
}

// NormalizeBaseURL validates a provider endpoint and removes one trailing slash.
func (p URLPolicy) NormalizeBaseURL(ctx context.Context, raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse provider URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Opaque != "" {
		return nil, fmt.Errorf("invalid provider URL")
	}
	if u.Scheme != "https" && !(p.DevelopmentAllowPrivate && u.Scheme == "http") {
		return nil, fmt.Errorf("provider URL must use HTTPS")
	}
	host := u.Hostname()
	if host == "" || strings.ContainsAny(host, "\x00") || !isASCII(host) {
		return nil, fmt.Errorf("invalid provider hostname")
	}
	if strings.HasSuffix(u.Host, ":") {
		return nil, fmt.Errorf("provider URL has an empty port")
	}
	if port := u.Port(); port != "" && !p.DevelopmentAllowPrivate && port != "443" {
		return nil, fmt.Errorf("provider URL has a non-canonical port")
	}
	if ip, err := netip.ParseAddr(host); err == nil && p.forbidden(ip) {
		return nil, fmt.Errorf("forbidden provider address %s", ip)
	} else if err == nil {
		return nil, fmt.Errorf("provider URL must use a hostname")
	}
	if _, err := p.ValidateResolved(ctx, host); err != nil {
		return nil, err
	}
	if u.Path != "/" {
		u.Path = strings.TrimSuffix(u.Path, "/")
	}
	return u, nil
}

// ValidateResolved resolves host and rejects any forbidden answer.  All DNS
// answers are checked so a mixed public/private answer cannot bypass policy.
func (p URLPolicy) ValidateResolved(ctx context.Context, host string) ([]netip.Addr, error) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" || !isASCII(host) || (host != "localhost" && strings.HasSuffix(host, ".local")) {
		return nil, fmt.Errorf("forbidden provider hostname %q", host)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if p.forbidden(ip) {
			return nil, fmt.Errorf("forbidden provider address %s", ip)
		}
		return []netip.Addr{ip}, nil
	}
	resolver := p.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	answers, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve provider hostname %q: %w", host, err)
	}
	if len(answers) == 0 {
		return nil, fmt.Errorf("provider hostname %q returned no addresses", host)
	}
	for _, ip := range answers {
		if p.forbidden(ip) {
			return nil, fmt.Errorf("forbidden provider address %s", ip)
		}
	}
	return answers, nil
}

func (p URLPolicy) forbidden(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() {
		return !p.DevelopmentAllowPrivate
	}
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	for _, prefix := range forbiddenPrefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

var forbiddenPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func isASCII(value string) bool {
	for _, r := range value {
		if r > 127 {
			return false
		}
	}
	return true
}
