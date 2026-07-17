// Package redisx contains Redis-backed controls that are optional for service
// availability but strengthen abuse resistance when Redis is reachable.
package redisx

import (
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// NewClient constructs a Redis client without pinging it. Redis is deliberately
// not a startup dependency: callers handle an unavailable server at operation
// time through bounded local protection.
func NewClient(rawURL string) (*redis.Client, error) {
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis URL: %w", err)
	}
	options.DialTimeout = 2 * time.Second
	options.ReadTimeout = 2 * time.Second
	options.WriteTimeout = 2 * time.Second
	options.MaxRetries = 0
	return redis.NewClient(options), nil
}
