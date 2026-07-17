package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLoadFromDefaults(t *testing.T) {
	cfg, err := LoadFrom(environment(map[string]string{"API_KEY": "secret"}))
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	if cfg.ListenAddr != ":8080" || cfg.APIKey != "secret" {
		t.Fatalf("unexpected basic config: %+v", cfg)
	}
	if got := cfg.APIBaseURL.String(); got != "https://anilibria.top/api/v1/" {
		t.Errorf("APIBaseURL = %q", got)
	}
	if got := cfg.SiteBaseURL.String(); got != "https://aniliberty.top/" {
		t.Errorf("SiteBaseURL = %q", got)
	}
	if cfg.RequestTimeout != 90*time.Second || cfg.HTTPTimeout != 15*time.Second || cfg.RequestInterval != 2100*time.Millisecond {
		t.Errorf("unexpected timeout defaults: %+v", cfg)
	}
	if cfg.MaxConcurrency != 4 || cfg.MaxReleasesPerSearch != 10 || cfg.MaxResponseBytes != 8*1024*1024 {
		t.Errorf("unexpected limit defaults: %+v", cfg)
	}
	if cfg.SearchCacheTTL != 10*time.Minute || cfg.TorrentsCacheTTL != 15*time.Minute || cfg.LatestCacheTTL != 5*time.Minute || cfg.NegativeCacheTTL != time.Minute || cfg.CacheMaxEntries != 1024 {
		t.Errorf("unexpected cache defaults: %+v", cfg)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v", cfg.LogLevel)
	}
}

func TestLoadFromParsesOverridesAndNormalizesURLs(t *testing.T) {
	cfg, err := LoadFrom(environment(map[string]string{
		"API_KEY":                 " exact key ",
		"LISTEN_ADDR":             "127.0.0.1:9090",
		"ANILIBRIA_API_BASE_URL":  "https://api.example.test/prefix",
		"ANILIBRIA_SITE_BASE_URL": "https://site.example.test///",
		"REQUEST_TIMEOUT":         "2m",
		"HTTP_TIMEOUT":            "2m",
		"REQUEST_INTERVAL":        "10ms",
		"MAX_CONCURRENCY":         "64",
		"MAX_RELEASES_PER_SEARCH": "50",
		"MAX_RESPONSE_BYTES":      "64MiB",
		"SEARCH_CACHE_TTL":        "1s",
		"TORRENTS_CACHE_TTL":      "24h",
		"LATEST_CACHE_TTL":        "2m",
		"NEGATIVE_CACHE_TTL":      "3m",
		"CACHE_MAX_ENTRIES":       "1000000",
		"LOG_LEVEL":               "WaRn",
	}))
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}

	if cfg.APIKey != " exact key " {
		t.Errorf("APIKey was altered")
	}
	if got := cfg.APIBaseURL.String(); got != "https://api.example.test/prefix/" {
		t.Errorf("APIBaseURL = %q", got)
	}
	if got := cfg.SiteBaseURL.String(); got != "https://site.example.test/" {
		t.Errorf("SiteBaseURL = %q", got)
	}
	if cfg.MaxResponseBytes != 64*1024*1024 || cfg.LogLevel != slog.LevelWarn {
		t.Errorf("unexpected parsed values: %+v", cfg)
	}
}

func TestLoadFromPreservesEncodedSlashesInURLPrefixes(t *testing.T) {
	cfg, err := LoadFrom(environment(map[string]string{
		"API_KEY":                 "secret",
		"ANILIBRIA_API_BASE_URL":  "https://api.example.test/proxy%2Ftenant/api/v1///",
		"ANILIBRIA_SITE_BASE_URL": "https://site.example.test/proxy%2Ftenant///",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.APIBaseURL.String(); got != "https://api.example.test/proxy%2Ftenant/api/v1/" {
		t.Errorf("APIBaseURL = %q", got)
	}
	if got := cfg.SiteBaseURL.String(); got != "https://site.example.test/proxy%2Ftenant/" {
		t.Errorf("SiteBaseURL = %q", got)
	}
}

func TestLoadFromRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		values  map[string]string
		wantVar string
	}{
		{name: "missing API key", values: map[string]string{}, wantVar: "API_KEY"},
		{name: "non-UTF-8 API key", values: map[string]string{"API_KEY": string([]byte{0xff})}, wantVar: "API_KEY"},
		{name: "empty listen address", values: map[string]string{"API_KEY": "x", "LISTEN_ADDR": " \t"}, wantVar: "LISTEN_ADDR"},
		{name: "zero listen port", values: map[string]string{"API_KEY": "x", "LISTEN_ADDR": ":0"}, wantVar: "LISTEN_ADDR"},
		{name: "padded zero listen port", values: map[string]string{"API_KEY": "x", "LISTEN_ADDR": ":000"}, wantVar: "LISTEN_ADDR"},
		{name: "IPv4 zero listen port", values: map[string]string{"API_KEY": "x", "LISTEN_ADDR": "0.0.0.0:0"}, wantVar: "LISTEN_ADDR"},
		{name: "IPv6 zero listen port", values: map[string]string{"API_KEY": "x", "LISTEN_ADDR": "[::]:0"}, wantVar: "LISTEN_ADDR"},
		{name: "missing listen port", values: map[string]string{"API_KEY": "x", "LISTEN_ADDR": "localhost"}, wantVar: "LISTEN_ADDR"},
		{name: "named listen port", values: map[string]string{"API_KEY": "x", "LISTEN_ADDR": ":http"}, wantVar: "LISTEN_ADDR"},
		{name: "overflowing listen port", values: map[string]string{"API_KEY": "x", "LISTEN_ADDR": ":65536"}, wantVar: "LISTEN_ADDR"},
		{name: "HTTP upstream", values: map[string]string{"API_KEY": "x", "ANILIBRIA_API_BASE_URL": "http://example.test/"}, wantVar: "ANILIBRIA_API_BASE_URL"},
		{name: "URL user info", values: map[string]string{"API_KEY": "x", "ANILIBRIA_SITE_BASE_URL": "https://user:pass@example.test/"}, wantVar: "ANILIBRIA_SITE_BASE_URL"},
		{name: "URL query", values: map[string]string{"API_KEY": "x", "ANILIBRIA_SITE_BASE_URL": "https://example.test/?x=1"}, wantVar: "ANILIBRIA_SITE_BASE_URL"},
		{name: "short HTTP timeout", values: map[string]string{"API_KEY": "x", "HTTP_TIMEOUT": "99ms"}, wantVar: "HTTP_TIMEOUT"},
		{name: "request shorter than HTTP", values: map[string]string{"API_KEY": "x", "REQUEST_TIMEOUT": "1s", "HTTP_TIMEOUT": "2s"}, wantVar: "REQUEST_TIMEOUT"},
		{name: "bad interval", values: map[string]string{"API_KEY": "x", "REQUEST_INTERVAL": "0s"}, wantVar: "REQUEST_INTERVAL"},
		{name: "bad concurrency", values: map[string]string{"API_KEY": "x", "MAX_CONCURRENCY": "0"}, wantVar: "MAX_CONCURRENCY"},
		{name: "bad fanout", values: map[string]string{"API_KEY": "x", "MAX_RELEASES_PER_SEARCH": "51"}, wantVar: "MAX_RELEASES_PER_SEARCH"},
		{name: "bad bytes suffix", values: map[string]string{"API_KEY": "x", "MAX_RESPONSE_BYTES": "8MB"}, wantVar: "MAX_RESPONSE_BYTES"},
		{name: "bytes overflow", values: map[string]string{"API_KEY": "x", "MAX_RESPONSE_BYTES": "9223372036854775807GiB"}, wantVar: "MAX_RESPONSE_BYTES"},
		{name: "bad cache TTL", values: map[string]string{"API_KEY": "x", "LATEST_CACHE_TTL": "25h"}, wantVar: "LATEST_CACHE_TTL"},
		{name: "bad cache entries", values: map[string]string{"API_KEY": "x", "CACHE_MAX_ENTRIES": "0"}, wantVar: "CACHE_MAX_ENTRIES"},
		{name: "bad log level", values: map[string]string{"API_KEY": "x", "LOG_LEVEL": "trace"}, wantVar: "LOG_LEVEL"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := LoadFrom(environment(test.values))
			if err == nil || !strings.Contains(err.Error(), test.wantVar) {
				t.Fatalf("LoadFrom() error = %v, want error naming %s", err, test.wantVar)
			}
		})
	}
}

func TestLoadFromErrorsDoNotDiscloseValues(t *testing.T) {
	const secret = "do-not-print-this-api-key"
	_, err := LoadFrom(environment(map[string]string{
		"API_KEY":      secret,
		"HTTP_TIMEOUT": "definitely-not-a-duration",
	}))
	if err == nil {
		t.Fatal("LoadFrom() error = nil")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "definitely-not-a-duration") {
		t.Fatalf("error disclosed configured value: %q", err)
	}

	tooLong := strings.Repeat("sensitive", 200)
	_, err = LoadFrom(environment(map[string]string{"API_KEY": tooLong}))
	if err == nil || strings.Contains(err.Error(), tooLong) {
		t.Fatalf("API key error disclosed value: %q", err)
	}
}

func TestByteSizeAcceptsIntegerBytesAndBinarySuffixes(t *testing.T) {
	tests := map[string]int64{
		"1024": 1024,
		"1KiB": 1024,
		"2MiB": 2 * 1024 * 1024,
		"0GiB": 0,
	}
	for raw, want := range tests {
		got, err := byteSize(environment(map[string]string{"SIZE": raw}), "SIZE", 0, 0, 64*1024*1024)
		if err != nil {
			t.Errorf("byteSize(%q) error = %v", raw, err)
		} else if got != want {
			t.Errorf("byteSize(%q) = %d, want %d", raw, got, want)
		}
	}
}

func environment(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
