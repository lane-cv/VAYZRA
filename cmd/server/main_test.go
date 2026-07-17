package main

import (
	"net/http"
	"testing"
	"time"
)

func TestNewServerUsesConfiguredAddressAndTimeouts(t *testing.T) {
	s := newServer(":9010", http.NotFoundHandler())
	if s.Addr != ":9010" {
		t.Fatalf("address = %q", s.Addr)
	}
	if s.ReadHeaderTimeout == 0 || s.ReadTimeout == 0 || s.WriteTimeout == 0 || s.IdleTimeout == 0 {
		t.Fatalf("expected explicit timeouts: %#v", s)
	}
	if s.ReadHeaderTimeout != 5*time.Second || s.ReadTimeout != 15*time.Second || s.WriteTimeout != 15*time.Second || s.IdleTimeout != 60*time.Second {
		t.Fatalf("unexpected timeouts: %#v", s)
	}
}
