package safelog

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"strings"
	"time"

	"happylearn.local/app/internal/platform/config"
)

var errInvalidSecretConfiguration = errors.New("safelog: invalid secret configuration")

// NewFromConfig constructs a logger whose redactor knows every secret held by
// the loaded application configuration. Optional secrets may be empty. Any
// configured secret that is too short for reliable redaction fails closed.
func NewFromConfig(output io.Writer, clock func() time.Time, cfg config.Config) (Logger, error) {
	collector := secretCollector{
		seen: make(map[string]struct{}),
	}

	if !collector.addRequired(cfg.DatabaseURL) ||
		!collector.addURLCredentials(cfg.DatabaseURL) ||
		!collector.addRequired(cfg.RedisURL) ||
		!collector.addURLCredentials(cfg.RedisURL) ||
		!collector.addRequired(cfg.LoginThrottleSecret) ||
		!collector.addRequired(cfg.MinIOAccessKey) ||
		!collector.addRequired(cfg.MinIOSecretKey) ||
		!collector.addRequiredBytes(cfg.AIMasterKey) ||
		!collector.addOptional(cfg.MetricsBearerSecret) ||
		!collector.addOptionalBytes(cfg.HostMetricsHMACSecret) ||
		!collector.addOptional(cfg.WebhookURL) ||
		!collector.addOptional(cfg.WebhookAuthorization) {
		return Logger{}, errInvalidSecretConfiguration
	}

	logger, err := New(output, clock, collector.values...)
	if err != nil {
		return Logger{}, errInvalidSecretConfiguration
	}
	return logger, nil
}

type secretCollector struct {
	seen   map[string]struct{}
	values []string
}

func (collector *secretCollector) addRequired(value string) bool {
	if len(value) < 8 {
		return false
	}
	collector.add(value)
	return true
}

func (collector *secretCollector) addOptional(value string) bool {
	if value == "" {
		return true
	}
	return collector.addRequired(value)
}

func (collector *secretCollector) addRequiredBytes(value []byte) bool {
	if len(value) < 8 {
		return false
	}
	collector.add(string(value))
	collector.add(base64.StdEncoding.EncodeToString(value))
	collector.add(hex.EncodeToString(value))
	return true
}

func (collector *secretCollector) addOptionalBytes(value []byte) bool {
	if len(value) == 0 {
		return true
	}
	return collector.addRequiredBytes(value)
}

func (collector *secretCollector) addURLCredentials(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return false
	}

	if parsed.User != nil {
		if password, present := parsed.User.Password(); present && !collector.addRequired(password) {
			return false
		}
	}
	if password, present := rawURLPassword(value); present && !collector.addRequired(password) {
		return false
	}

	for _, pair := range strings.Split(parsed.RawQuery, "&") {
		rawName, rawValue, present := strings.Cut(pair, "=")
		name, err := url.QueryUnescape(rawName)
		if err != nil {
			return false
		}
		if !sensitiveQueryName(name) {
			continue
		}
		if !present || !collector.addRequired(rawValue) {
			return false
		}
		queryValue, err := url.QueryUnescape(rawValue)
		if err != nil || !collector.addRequired(queryValue) {
			return false
		}
	}
	return true
}

func rawURLPassword(value string) (string, bool) {
	schemeEnd := strings.Index(value, "://")
	if schemeEnd < 0 {
		return "", false
	}
	authority := value[schemeEnd+3:]
	if authorityEnd := strings.IndexAny(authority, "/?#"); authorityEnd >= 0 {
		authority = authority[:authorityEnd]
	}
	at := strings.LastIndexByte(authority, '@')
	if at < 0 {
		return "", false
	}
	_, password, present := strings.Cut(authority[:at], ":")
	return password, present
}

func (collector *secretCollector) add(value string) {
	if _, exists := collector.seen[value]; exists {
		return
	}
	collector.seen[value] = struct{}{}
	collector.values = append(collector.values, value)
}

func sensitiveQueryName(name string) bool {
	lower := strings.ToLower(name)
	for _, fragment := range []string{
		"password",
		"secret",
		"token",
		"credential",
		"authorization",
		"key",
		"access_key",
		"access-key",
		"apikey",
		"api_key",
		"api-key",
	} {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}
