package config

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	Environment         string
	ListenAddress       string
	DatabaseURL         string
	RedisURL            string
	LoginThrottleSecret string
	TrustedProxyCIDRs   []netip.Prefix
	PublicOrigin        string
	SessionIdleTTL      time.Duration
	SessionAbsoluteTTL  time.Duration
	CookieSecure        bool
}

func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Environment:        "development",
		ListenAddress:      ":8080",
		SessionIdleTTL:     7 * 24 * time.Hour,
		SessionAbsoluteTTL: 30 * 24 * time.Hour,
	}
	if v := getenv("HAPPYLEARN_ENV"); v != "" {
		c.Environment = v
	}
	if v := getenv("HAPPYLEARN_LISTEN"); v != "" {
		c.ListenAddress = v
	}
	if c.Environment != "development" && c.Environment != "production" {
		return Config{}, fmt.Errorf("HAPPYLEARN_ENV must be development or production")
	}
	c.DatabaseURL = getenv("HAPPYLEARN_DATABASE_URL")
	c.RedisURL = getenv("HAPPYLEARN_REDIS_URL")
	c.LoginThrottleSecret = getenv("HAPPYLEARN_LOGIN_THROTTLE_SECRET")
	if raw := getenv("HAPPYLEARN_TRUSTED_PROXY_CIDRS"); raw != "" {
		for _, value := range strings.Split(raw, ",") {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
			if err != nil {
				return Config{}, fmt.Errorf("HAPPYLEARN_TRUSTED_PROXY_CIDRS is invalid")
			}
			c.TrustedProxyCIDRs = append(c.TrustedProxyCIDRs, prefix)
		}
	}

	c.PublicOrigin = getenv("HAPPYLEARN_PUBLIC_ORIGIN")
	if c.PublicOrigin != "" {
		var originErr error
		c.PublicOrigin, originErr = normalizePublicOrigin(c.PublicOrigin)
		if originErr != nil {
			return Config{}, fmt.Errorf("HAPPYLEARN_PUBLIC_ORIGIN is invalid")
		}
		if c.Environment == "production" && !strings.HasPrefix(c.PublicOrigin, "https://") {
			return Config{}, fmt.Errorf("HAPPYLEARN_PUBLIC_ORIGIN must use https in production")
		}
	}
	c.CookieSecure = c.Environment == "production"

	for _, required := range []struct {
		name  string
		value string
	}{
		{"HAPPYLEARN_DATABASE_URL", c.DatabaseURL},
		{"HAPPYLEARN_REDIS_URL", c.RedisURL},
		{"HAPPYLEARN_LOGIN_THROTTLE_SECRET", c.LoginThrottleSecret},
		{"HAPPYLEARN_PUBLIC_ORIGIN", c.PublicOrigin},
	} {
		if required.value == "" {
			return Config{}, fmt.Errorf("%s is required", required.name)
		}
	}
	if len(c.LoginThrottleSecret) < 32 {
		return Config{}, fmt.Errorf("HAPPYLEARN_LOGIN_THROTTLE_SECRET must be at least 32 bytes")
	}

	return c, nil
}

func normalizePublicOrigin(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw || strings.Contains(raw, ",") {
		return "", fmt.Errorf("invalid origin")
	}
	u, err := url.ParseRequestURI(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == "" {
		return "", fmt.Errorf("invalid origin")
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return u.Scheme + "://" + host, nil
}
