package config

import (
	"fmt"
	"strings"
	"time"
)

type Config struct {
	Environment        string
	ListenAddress      string
	DatabaseURL        string
	RedisURL           string
	PublicOrigin       string
	SessionIdleTTL     time.Duration
	SessionAbsoluteTTL time.Duration
	CookieSecure       bool
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
	c.DatabaseURL = getenv("HAPPYLEARN_DATABASE_URL")
	c.RedisURL = getenv("HAPPYLEARN_REDIS_URL")
	c.PublicOrigin = strings.TrimRight(getenv("HAPPYLEARN_PUBLIC_ORIGIN"), "/")
	c.CookieSecure = c.Environment == "production"

	for _, required := range []struct {
		name  string
		value string
	}{
		{"HAPPYLEARN_DATABASE_URL", c.DatabaseURL},
		{"HAPPYLEARN_REDIS_URL", c.RedisURL},
		{"HAPPYLEARN_PUBLIC_ORIGIN", c.PublicOrigin},
	} {
		if required.value == "" {
			return Config{}, fmt.Errorf("%s is required", required.name)
		}
	}

	return c, nil
}
