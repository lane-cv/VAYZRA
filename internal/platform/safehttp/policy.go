// Package safehttp provides an outbound HTTP policy and transport that pin
// validated DNS answers and reject non-public destinations.
package safehttp

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const defaultConnectTimeout = 5 * time.Second

// Resolver resolves network addresses for an outbound hostname.
type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// Policy limits outbound requests to publicly routable HTTPS endpoints.
type Policy struct {
	DevelopmentAllowPrivate bool
	Resolver                Resolver
}

// NormalizeBaseURL validates an outbound endpoint and removes one trailing
// slash. Base URLs cannot contain credentials, queries, or fragments.
func (p Policy) NormalizeBaseURL(ctx context.Context, raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse provider URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery || u.Opaque != "" || strings.Contains(raw, "#") {
		return nil, fmt.Errorf("invalid provider URL")
	}
	if u.Scheme != "https" && !(p.DevelopmentAllowPrivate && u.Scheme == "http") {
		return nil, fmt.Errorf("provider URL must use HTTPS")
	}
	host := u.Hostname()
	if host == "" || strings.ContainsAny(host, "\x00%") || !isASCII(host) {
		return nil, fmt.Errorf("invalid provider hostname")
	}
	if strings.HasSuffix(u.Host, ":") {
		return nil, fmt.Errorf("provider URL has an empty port")
	}
	if port := u.Port(); port != "" && !p.DevelopmentAllowPrivate && port != "443" {
		return nil, fmt.Errorf("provider URL has a non-canonical port")
	}
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil && p.forbidden(ip) {
		return nil, fmt.Errorf("forbidden provider address %s", ip)
	} else if parseErr == nil {
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

// ValidateResolved resolves host and rejects every answer if any answer is not
// publicly routable. Checking the full answer set prevents mixed-answer SSRF.
func (p Policy) ValidateResolved(ctx context.Context, host string) ([]netip.Addr, error) {
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
	resolveCtx := ctx
	cancel := func() {}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		resolveCtx, cancel = context.WithTimeout(ctx, defaultConnectTimeout)
	}
	defer cancel()
	answers, err := resolver.LookupNetIP(resolveCtx, "ip", host)
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

// ValidateRequestURL applies the authority and network policy to a request URL.
// Query strings are allowed because provider APIs may use them.
func (p Policy) ValidateRequestURL(ctx context.Context, u *url.URL) ([]netip.Addr, error) {
	if u == nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || strings.Contains(u.String(), "#") {
		return nil, fmt.Errorf("invalid provider request URL")
	}
	if u.Scheme != "https" && !(p.DevelopmentAllowPrivate && u.Scheme == "http") {
		return nil, fmt.Errorf("provider request URL must use HTTPS")
	}
	host := u.Hostname()
	if host == "" || strings.ContainsAny(host, "\x00%") || !isASCII(host) {
		return nil, fmt.Errorf("invalid provider request hostname")
	}
	if strings.HasSuffix(u.Host, ":") {
		return nil, fmt.Errorf("provider request URL has an empty port")
	}
	if port := u.Port(); port != "" && !p.DevelopmentAllowPrivate && port != "443" {
		return nil, fmt.Errorf("provider request URL has a non-canonical port")
	}
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil && p.forbidden(ip) {
		return nil, fmt.Errorf("forbidden provider address %s", ip)
	} else if parseErr == nil {
		return nil, fmt.Errorf("provider request URL must use a hostname")
	}
	return p.ValidateResolved(ctx, host)
}

func (p Policy) forbidden(ip netip.Addr) bool {
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
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.31.196.0/24"),
	netip.MustParsePrefix("192.52.193.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.175.48.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/20"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2620:4f:8000::/48"),
	netip.MustParsePrefix("3ffe::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

func isASCII(value string) bool {
	for _, r := range value {
		if r > 127 {
			return false
		}
	}
	return true
}
