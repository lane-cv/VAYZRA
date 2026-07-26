package config

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const developmentAIMasterKey = "happylearn-dev-ai-master-key-000"

type Config struct {
	Environment             string
	ListenAddress           string
	DatabaseURL             string
	RedisURL                string
	LoginThrottleSecret     string
	TrustedProxyCIDRs       []netip.Prefix
	PublicOrigin            string
	SessionIdleTTL          time.Duration
	SessionAbsoluteTTL      time.Duration
	CookieSecure            bool
	MinIOEndpoint           string
	MinIOAccessKey          string
	MinIOSecretKey          string
	MinIOUseTLS             bool
	MinIOOriginalsBucket    string
	MinIOPreviewsBucket     string
	AIMasterKey             []byte
	AIMasterKeyVersion      int16
	AIBusinessTimezone      string
	AIGlobalConcurrency     int
	AIPerStudentConcurrency int
	AIAllowPrivateProvider  bool
}

func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Environment:             "development",
		ListenAddress:           ":8080",
		SessionIdleTTL:          7 * 24 * time.Hour,
		SessionAbsoluteTTL:      30 * 24 * time.Hour,
		AIMasterKeyVersion:      1,
		AIBusinessTimezone:      "Asia/Shanghai",
		AIGlobalConcurrency:     2,
		AIPerStudentConcurrency: 1,
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
	if raw := getenv("HAPPYLEARN_AI_MASTER_KEY"); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || len(key) != 32 || base64.StdEncoding.EncodeToString(key) != raw {
			return Config{}, fmt.Errorf("HAPPYLEARN_AI_MASTER_KEY must be standard base64 of exactly 32 bytes")
		}
		c.AIMasterKey = key
	} else if c.Environment == "development" {
		c.AIMasterKey = []byte(developmentAIMasterKey)
	} else {
		return Config{}, fmt.Errorf("HAPPYLEARN_AI_MASTER_KEY is required in production")
	}
	if raw := getenv("HAPPYLEARN_AI_MASTER_KEY_VERSION"); raw != "" {
		version, err := strconv.ParseInt(raw, 10, 16)
		if err != nil || version < 1 {
			return Config{}, fmt.Errorf("HAPPYLEARN_AI_MASTER_KEY_VERSION must be between 1 and 32767")
		}
		c.AIMasterKeyVersion = int16(version)
	}
	if raw := getenv("HAPPYLEARN_AI_BUSINESS_TIMEZONE"); raw != "" {
		if raw != "Asia/Shanghai" {
			return Config{}, fmt.Errorf("HAPPYLEARN_AI_BUSINESS_TIMEZONE must be Asia/Shanghai")
		}
		c.AIBusinessTimezone = raw
	}
	if raw := getenv("HAPPYLEARN_AI_GLOBAL_CONCURRENCY"); raw != "" {
		globalConcurrency, err := strconv.Atoi(raw)
		if err != nil || globalConcurrency < 1 || globalConcurrency > 8 {
			return Config{}, fmt.Errorf("HAPPYLEARN_AI_GLOBAL_CONCURRENCY must be between 1 and 8")
		}
		c.AIGlobalConcurrency = globalConcurrency
	}
	if raw := getenv("HAPPYLEARN_AI_PER_STUDENT_CONCURRENCY"); raw != "" {
		perStudentConcurrency, err := strconv.Atoi(raw)
		if err != nil || perStudentConcurrency < 1 || perStudentConcurrency > c.AIGlobalConcurrency {
			return Config{}, fmt.Errorf("HAPPYLEARN_AI_PER_STUDENT_CONCURRENCY must be between 1 and HAPPYLEARN_AI_GLOBAL_CONCURRENCY")
		}
		c.AIPerStudentConcurrency = perStudentConcurrency
	}
	if raw := getenv("HAPPYLEARN_AI_ALLOW_PRIVATE_PROVIDER"); raw != "" {
		switch raw {
		case "false":
		case "true":
			if c.Environment != "development" {
				return Config{}, fmt.Errorf("HAPPYLEARN_AI_ALLOW_PRIVATE_PROVIDER may only be true in development")
			}
			c.AIAllowPrivateProvider = true
		default:
			return Config{}, fmt.Errorf("HAPPYLEARN_AI_ALLOW_PRIVATE_PROVIDER must be true or false")
		}
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
	c.MinIOEndpoint = getenv("HAPPYLEARN_MINIO_ENDPOINT")
	c.MinIOAccessKey = getenv("HAPPYLEARN_MINIO_ACCESS_KEY")
	c.MinIOSecretKey = getenv("HAPPYLEARN_MINIO_SECRET_KEY")
	c.MinIOOriginalsBucket = getenv("HAPPYLEARN_MINIO_ORIGINALS_BUCKET")
	c.MinIOPreviewsBucket = getenv("HAPPYLEARN_MINIO_PREVIEWS_BUCKET")
	if c.Environment == "development" {
		if c.MinIOEndpoint == "" {
			c.MinIOEndpoint = "127.0.0.1:59000"
		}
		if c.MinIOAccessKey == "" {
			c.MinIOAccessKey = "happylearn_dev"
		}
		if c.MinIOSecretKey == "" {
			c.MinIOSecretKey = "happylearn_minio_dev_secret"
		}
		if c.MinIOOriginalsBucket == "" {
			c.MinIOOriginalsBucket = "happylearn-originals"
		}
		if c.MinIOPreviewsBucket == "" {
			c.MinIOPreviewsBucket = "happylearn-previews"
		}
	}
	if raw := getenv("HAPPYLEARN_MINIO_USE_TLS"); raw != "" {
		useTLS, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("HAPPYLEARN_MINIO_USE_TLS must be true or false")
		}
		c.MinIOUseTLS = useTLS
	}
	if c.MinIOEndpoint == "" {
		return Config{}, fmt.Errorf("HAPPYLEARN_MINIO_ENDPOINT is required in production")
	}
	if c.MinIOAccessKey == "" {
		return Config{}, fmt.Errorf("HAPPYLEARN_MINIO_ACCESS_KEY is required in production")
	}
	if c.MinIOSecretKey == "" {
		return Config{}, fmt.Errorf("HAPPYLEARN_MINIO_SECRET_KEY is required in production")
	}
	if c.MinIOOriginalsBucket == "" {
		return Config{}, fmt.Errorf("HAPPYLEARN_MINIO_ORIGINALS_BUCKET is required in production")
	}
	if c.MinIOPreviewsBucket == "" {
		return Config{}, fmt.Errorf("HAPPYLEARN_MINIO_PREVIEWS_BUCKET is required in production")
	}
	if !validMinIOEndpoint(c.MinIOEndpoint) {
		return Config{}, fmt.Errorf("HAPPYLEARN_MINIO_ENDPOINT must be a host and optional port without scheme, credentials, path, query, or fragment")
	}
	for _, bucket := range []struct {
		name  string
		value string
	}{
		{"HAPPYLEARN_MINIO_ORIGINALS_BUCKET", c.MinIOOriginalsBucket},
		{"HAPPYLEARN_MINIO_PREVIEWS_BUCKET", c.MinIOPreviewsBucket},
	} {
		if !validS3BucketName(bucket.value) {
			return Config{}, fmt.Errorf("%s must be a valid S3 bucket name", bucket.name)
		}
	}
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

var s3BucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

func validMinIOEndpoint(raw string) bool {
	if raw == "" || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "/?#,") {
		return false
	}
	u, err := url.Parse("http://" + raw)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == "" {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return false
		}
	}
	return true
}

func validS3BucketName(name string) bool {
	if !s3BucketName.MatchString(name) || strings.Contains(name, "..") || strings.Contains(name, ".-") || strings.Contains(name, "-.") || net.ParseIP(name) != nil {
		return false
	}
	for _, prefix := range []string{"xn--", "sthree-", "amzn-s3-demo-"} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	for _, suffix := range []string{"-s3alias", "--ol-s3", ".mrap", "--x-s3", "--table-s3"} {
		if strings.HasSuffix(name, suffix) {
			return false
		}
	}
	return true
}
