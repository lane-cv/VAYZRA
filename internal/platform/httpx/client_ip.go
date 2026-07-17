package httpx

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIP resolves a direct client or a client reported by explicitly trusted
// reverse proxies. Forwarding headers are ignored unless the immediate peer is
// trusted; malformed trusted-proxy input is rejected rather than guessed.
func ClientIP(r *http.Request, trusted []netip.Prefix) (netip.Addr, error) {
	peer, err := remoteAddr(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("invalid remote address")
	}
	if !isTrusted(peer, trusted) {
		return peer, nil
	}
	values := r.Header.Values("X-Forwarded-For")
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return netip.Addr{}, fmt.Errorf("invalid forwarded address")
	}
	parts := strings.Split(values[0], ",")
	chain := make([]netip.Addr, 0, len(parts))
	for _, part := range parts {
		addr, err := netip.ParseAddr(strings.TrimSpace(part))
		if err != nil {
			return netip.Addr{}, fmt.Errorf("invalid forwarded address")
		}
		chain = append(chain, addr.Unmap())
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if !isTrusted(chain[i], trusted) {
			return chain[i], nil
		}
	}
	return chain[0], nil
}
func remoteAddr(value string) (netip.Addr, error) {
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		return netip.Addr{}, err
	}
	addr, err := netip.ParseAddr(host)
	return addr.Unmap(), err
}
func isTrusted(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, prefix := range trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
