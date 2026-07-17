package httpx

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIPTrustedProxyBoundaries(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("2001:db8::/32")}
	for _, tc := range []struct {
		name, remote, forwarded, want string
		invalid                       bool
	}{
		{"direct ignores spoof", "192.0.2.9:123", "198.51.100.4", "192.0.2.9", false},
		{"trusted proxy", "10.1.2.3:443", "198.51.100.4", "198.51.100.4", false},
		{"trusted chain", "10.1.2.3:443", "198.51.100.4, 10.2.3.4", "198.51.100.4", false},
		{"ipv6", "[2001:db8::2]:443", "2001:db8:1::4, 2001:db8::3", "2001:db8:1::4", false},
		{"malformed", "10.1.2.3:443", "bad", "", true},
		{"duplicate headers", "10.1.2.3:443", "198.51.100.4", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", nil)
			r.RemoteAddr = tc.remote
			if tc.forwarded != "" {
				r.Header.Add("X-Forwarded-For", tc.forwarded)
				if tc.name == "duplicate headers" {
					r.Header.Add("X-Forwarded-For", tc.forwarded)
				}
			}
			got, err := ClientIP(r, trusted)
			if tc.invalid {
				if err == nil {
					t.Fatal("expected invalid forwarding error")
				}
				return
			}
			if err != nil || got.String() != tc.want {
				t.Fatalf("got=%v err=%v", got, err)
			}
		})
	}
}
