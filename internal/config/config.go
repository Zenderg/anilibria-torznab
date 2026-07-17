// Package config parses and validates the service's startup configuration.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultListenAddr           = ":8080"
	defaultAPIBaseURL           = "https://anilibria.top/api/v1/"
	defaultSiteBaseURL          = "https://aniliberty.top/"
	defaultRequestTimeout       = 90 * time.Second
	defaultHTTPTimeout          = 15 * time.Second
	defaultRequestInterval      = 2100 * time.Millisecond
	defaultMaxConcurrency       = 4
	defaultMaxReleasesPerSearch = 10
	defaultMaxResponseBytes     = 8 * 1024 * 1024
	defaultSearchCacheTTL       = 10 * time.Minute
	defaultTorrentsCacheTTL     = 15 * time.Minute
	defaultLatestCacheTTL       = 5 * time.Minute
	defaultNegativeCacheTTL     = time.Minute
	defaultCacheMaxEntries      = 1024
)

// Config contains validated startup configuration. URL values are pointers so
// tests can construct a Config with an httptest HTTP URL without weakening the
// HTTPS-only environment parser used in production.
type Config struct {
	ListenAddr string
	APIKey     string

	APIBaseURL  *url.URL
	SiteBaseURL *url.URL

	RequestTimeout  time.Duration
	HTTPTimeout     time.Duration
	RequestInterval time.Duration

	MaxConcurrency       int
	MaxReleasesPerSearch int
	MaxResponseBytes     int64

	SearchCacheTTL   time.Duration
	TorrentsCacheTTL time.Duration
	LatestCacheTTL   time.Duration
	NegativeCacheTTL time.Duration
	CacheMaxEntries  int

	LogLevel slog.Level
}

// Load reads configuration from the process environment.
func Load() (Config, error) {
	return LoadFrom(os.LookupEnv)
}

// LoadFrom reads configuration through getenv. It is intended for hermetic
// tests and applies exactly the same validation as Load.
func LoadFrom(getenv func(string) (string, bool)) (Config, error) {
	if getenv == nil {
		return Config{}, errors.New("configuration environment reader is nil")
	}

	var cfg Config
	var err error

	cfg.ListenAddr = optionalString(getenv, "LISTEN_ADDR", defaultListenAddr)
	if err := validateListenAddr(cfg.ListenAddr); err != nil {
		return Config{}, invalid("LISTEN_ADDR", err.Error())
	}

	cfg.APIKey, _ = getenv("API_KEY")
	if len(cfg.APIKey) < 1 || len(cfg.APIKey) > 1024 || !utf8.ValidString(cfg.APIKey) {
		return Config{}, invalid("API_KEY", "must contain 1..1024 bytes of valid UTF-8")
	}

	cfg.APIBaseURL, err = parseHTTPSBaseURL(optionalString(getenv, "ANILIBRIA_API_BASE_URL", defaultAPIBaseURL))
	if err != nil {
		return Config{}, invalid("ANILIBRIA_API_BASE_URL", err.Error())
	}
	cfg.SiteBaseURL, err = parseHTTPSBaseURL(optionalString(getenv, "ANILIBRIA_SITE_BASE_URL", defaultSiteBaseURL))
	if err != nil {
		return Config{}, invalid("ANILIBRIA_SITE_BASE_URL", err.Error())
	}

	if cfg.RequestTimeout, err = duration(getenv, "REQUEST_TIMEOUT", defaultRequestTimeout, time.Second, 10*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.HTTPTimeout, err = duration(getenv, "HTTP_TIMEOUT", defaultHTTPTimeout, 100*time.Millisecond, 2*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.RequestTimeout < cfg.HTTPTimeout {
		return Config{}, invalid("REQUEST_TIMEOUT", "must not be shorter than HTTP_TIMEOUT")
	}
	if cfg.RequestInterval, err = duration(getenv, "REQUEST_INTERVAL", defaultRequestInterval, 10*time.Millisecond, time.Minute); err != nil {
		return Config{}, err
	}

	if cfg.MaxConcurrency, err = integer(getenv, "MAX_CONCURRENCY", defaultMaxConcurrency, 1, 64); err != nil {
		return Config{}, err
	}
	if cfg.MaxReleasesPerSearch, err = integer(getenv, "MAX_RELEASES_PER_SEARCH", defaultMaxReleasesPerSearch, 1, 50); err != nil {
		return Config{}, err
	}
	if cfg.MaxResponseBytes, err = byteSize(getenv, "MAX_RESPONSE_BYTES", defaultMaxResponseBytes, 1024, 64*1024*1024); err != nil {
		return Config{}, err
	}

	if cfg.SearchCacheTTL, err = duration(getenv, "SEARCH_CACHE_TTL", defaultSearchCacheTTL, time.Second, 24*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.TorrentsCacheTTL, err = duration(getenv, "TORRENTS_CACHE_TTL", defaultTorrentsCacheTTL, time.Second, 24*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.LatestCacheTTL, err = duration(getenv, "LATEST_CACHE_TTL", defaultLatestCacheTTL, time.Second, 24*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.NegativeCacheTTL, err = duration(getenv, "NEGATIVE_CACHE_TTL", defaultNegativeCacheTTL, time.Second, 24*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.CacheMaxEntries, err = integer(getenv, "CACHE_MAX_ENTRIES", defaultCacheMaxEntries, 1, 1_000_000); err != nil {
		return Config{}, err
	}

	cfg.LogLevel, err = logLevel(optionalString(getenv, "LOG_LEVEL", "info"))
	if err != nil {
		return Config{}, invalid("LOG_LEVEL", err.Error())
	}

	return cfg, nil
}

func validateListenAddr(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("must be non-empty")
	}
	_, port, err := net.SplitHostPort(raw)
	if err != nil {
		return errors.New("must be a TCP host:port address")
	}
	value, err := strconv.ParseUint(port, 10, 16)
	if err != nil || value == 0 {
		return errors.New("must use a numeric TCP port between 1 and 65535")
	}
	return nil
}

func optionalString(getenv func(string) (string, bool), name, fallback string) string {
	value, ok := getenv(name)
	if !ok {
		return fallback
	}
	return value
}

func duration(getenv func(string) (string, bool), name string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	raw, ok := getenv(name)
	if !ok {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, invalid(name, "must use Go duration syntax")
	}
	if value < minimum || value > maximum {
		return 0, invalid(name, fmt.Sprintf("must be between %s and %s", minimum, maximum))
	}
	return value, nil
}

func integer(getenv func(string) (string, bool), name string, fallback, minimum, maximum int) (int, error) {
	raw, ok := getenv(name)
	if !ok {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, invalid(name, "must be an integer")
	}
	if value < minimum || value > maximum {
		return 0, invalid(name, fmt.Sprintf("must be between %d and %d", minimum, maximum))
	}
	return value, nil
}

func byteSize(getenv func(string) (string, bool), name string, fallback, minimum, maximum int64) (int64, error) {
	raw, ok := getenv(name)
	if !ok {
		return fallback, nil
	}

	multiplier := int64(1)
	number := raw
	for suffix, factor := range map[string]int64{
		"KiB": 1024,
		"MiB": 1024 * 1024,
		"GiB": 1024 * 1024 * 1024,
	} {
		if strings.HasSuffix(raw, suffix) {
			multiplier = factor
			number = strings.TrimSuffix(raw, suffix)
			break
		}
	}
	if number == "" {
		return 0, invalid(name, "must be an integer byte count optionally followed by KiB, MiB, or GiB")
	}
	value, err := strconv.ParseInt(number, 10, 64)
	if err != nil || value < 0 || value > math.MaxInt64/multiplier {
		return 0, invalid(name, "must be an integer byte count optionally followed by KiB, MiB, or GiB")
	}
	value *= multiplier
	if value < minimum || value > maximum {
		return 0, invalid(name, "must be between 1KiB and 64MiB")
	}
	return value, nil
}

func parseHTTPSBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("must be a valid absolute HTTPS URL")
	}
	if !parsed.IsAbs() || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" || parsed.Opaque != "" {
		return nil, errors.New("must be an absolute HTTPS URL with a host")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, errors.New("must not contain user info, a query, or a fragment")
	}

	if parsed.RawPath != "" {
		parsed.RawPath = strings.TrimRight(parsed.EscapedPath(), "/") + "/"
		parsed.Path, _ = url.PathUnescape(parsed.RawPath)
	} else {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/"
	}
	return parsed, nil
}

func logLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(raw) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, errors.New("must be debug, info, warn, or error")
	}
}

func invalid(name, reason string) error {
	return fmt.Errorf("invalid %s: %s", name, reason)
}
