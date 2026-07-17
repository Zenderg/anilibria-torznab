package anilibria

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	latestLimit     = 50
	maximumAttempts = 3
	backoffBase     = 250 * time.Millisecond
	backoffMaximum  = 5 * time.Second

	torrentInclude               = "hash,size,label,magnet,seeders,leechers,completed_times,updated_at,release.id,release.type.value,release.year,release.name.main,release.alias"
	invalidTorrentLogSampleLimit = 5
)

// API is the upstream boundary consumed by the service layer.
type API interface {
	SearchReleases(context.Context, string) ([]ReleaseID, error)
	TorrentsByRelease(context.Context, ReleaseID) ([]Torrent, error)
	Latest(context.Context) ([]Torrent, error)
}

// Config configures one process-wide AniLiberty client. The concurrency gate
// and attempt-start limiter are shared by every operation and retry on Client.
type Config struct {
	BaseURL          string
	Version          string
	HTTPClient       *http.Client
	HTTPTimeout      time.Duration
	RequestInterval  time.Duration
	MaxConcurrency   int
	MaxResponseBytes int64
	Logger           *slog.Logger
}

// Client is safe for concurrent use.
type Client struct {
	baseURL          *url.URL
	httpClient       *http.Client
	httpTimeout      time.Duration
	maxResponseBytes int64
	userAgent        string
	logger           *slog.Logger
	gate             chan struct{}
	limiter          intervalLimiter
	randomFloat      func() float64
	attemptStarted   func(time.Time)
}

type intervalLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

// NewClient constructs a client. HTTP base URLs are accepted for hermetic
// tests; production HTTPS enforcement belongs to startup configuration.
func NewClient(config Config) (*Client, error) {
	baseURL, err := url.Parse(config.BaseURL)
	if err != nil || baseURL.Host == "" || (baseURL.Scheme != "https" && baseURL.Scheme != "http") {
		return nil, fmt.Errorf("invalid AniLiberty API base URL")
	}
	if baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, fmt.Errorf("invalid AniLiberty API base URL")
	}
	if config.HTTPTimeout <= 0 {
		return nil, fmt.Errorf("HTTP timeout must be positive")
	}
	if config.RequestInterval <= 0 {
		return nil, fmt.Errorf("request interval must be positive")
	}
	if config.MaxConcurrency <= 0 {
		return nil, fmt.Errorf("maximum concurrency must be positive")
	}
	if config.MaxResponseBytes <= 0 {
		return nil, fmt.Errorf("maximum response size must be positive")
	}
	version := config.Version
	if version == "" {
		version = "dev"
	}
	for _, char := range version {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-') {
			return nil, fmt.Errorf("invalid application version")
		}
	}
	if baseURL.RawPath != "" {
		baseURL.RawPath = strings.TrimRight(baseURL.EscapedPath(), "/") + "/"
		baseURL.Path, _ = url.PathUnescape(baseURL.RawPath)
	} else {
		baseURL.Path = strings.TrimRight(baseURL.Path, "/") + "/"
	}

	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	clientCopy := *httpClient
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	return &Client{
		baseURL:          baseURL,
		httpClient:       &clientCopy,
		httpTimeout:      config.HTTPTimeout,
		maxResponseBytes: config.MaxResponseBytes,
		userAgent:        "anilibria-torznab/" + version + " (+https://github.com/Zenderg/anilibria-torznab)",
		logger:           logger,
		gate:             make(chan struct{}, config.MaxConcurrency),
		limiter: intervalLimiter{
			interval: config.RequestInterval,
		},
		randomFloat: rand.Float64,
	}, nil
}

// SearchReleases returns positive release IDs in upstream order.
func (client *Client) SearchReleases(ctx context.Context, query string) ([]ReleaseID, error) {
	if strings.TrimSpace(query) == "" {
		return nil, client.responseError("search_releases", fmt.Errorf("query must not be blank"))
	}
	requestURL := client.endpoint("app/search/releases", url.Values{
		"query":   {query},
		"include": {"id"},
	})
	var ids []ReleaseID
	if err := client.getJSON(ctx, "search_releases", requestURL, func(ctx context.Context, body []byte) error {
		trimmed := bytes.TrimSpace(body)
		if len(trimmed) == 0 || trimmed[0] != '[' {
			return fmt.Errorf("release search response is not an array")
		}
		var raw []rawReleaseID
		if err := decodeJSON(ctx, trimmed, &raw); err != nil {
			return err
		}
		ids = make([]ReleaseID, len(raw))
		for index, item := range raw {
			if err := ctx.Err(); err != nil {
				return err
			}
			id, err := positiveInteger(item.ID)
			if err != nil {
				return fmt.Errorf("invalid release id at index %d", index)
			}
			ids[index] = ReleaseID(id)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return ids, nil
}

// TorrentsByRelease returns valid torrent variants for one release. Invalid
// items are omitted and logged only by item index, release ID, and field name.
func (client *Client) TorrentsByRelease(ctx context.Context, releaseID ReleaseID) ([]Torrent, error) {
	if releaseID <= 0 {
		return nil, client.responseError("torrents_by_release", fmt.Errorf("release id must be positive"))
	}
	requestURL := client.endpoint("anime/torrents/release/"+strconv.FormatInt(int64(releaseID), 10), url.Values{
		"include": {torrentInclude},
	})
	var torrents []Torrent
	if err := client.getJSON(ctx, "torrents_by_release", requestURL, func(ctx context.Context, body []byte) error {
		var raw []rawTorrent
		if err := decodeTorrentCollection(ctx, body, &raw); err != nil {
			return err
		}
		var err error
		torrents, err = client.validTorrents(ctx, "torrents_by_release", raw)
		return err
	}); err != nil {
		return nil, err
	}
	return torrents, nil
}

// Latest returns the first, fixed 50-item AniLiberty torrent window.
func (client *Client) Latest(ctx context.Context) ([]Torrent, error) {
	requestURL := client.endpoint("anime/torrents", url.Values{
		"limit":   {strconv.Itoa(latestLimit)},
		"include": {torrentInclude},
	})
	var torrents []Torrent
	if err := client.getJSON(ctx, "latest", requestURL, func(ctx context.Context, body []byte) error {
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := decodeJSON(ctx, body, &envelope); err != nil {
			return err
		}
		trimmed := bytes.TrimSpace(envelope.Data)
		if len(trimmed) == 0 || trimmed[0] != '[' {
			return fmt.Errorf("latest data is not an array")
		}
		var raw []rawTorrent
		if err := decodeJSON(ctx, trimmed, &raw); err != nil {
			return err
		}
		var err error
		torrents, err = client.validTorrents(ctx, "latest", raw)
		return err
	}); err != nil {
		return nil, err
	}
	return torrents, nil
}

func (client *Client) validTorrents(ctx context.Context, operation string, raw []rawTorrent) ([]Torrent, error) {
	torrents := make([]Torrent, 0, len(raw))
	invalidCount := 0
	for index, item := range raw {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		torrent, field, err := validateTorrent(item)
		if err != nil {
			invalidCount++
			if invalidCount <= invalidTorrentLogSampleLimit {
				attributes := []any{"operation", operation, "item_index", index, "field", field}
				if releaseID, parseErr := positiveInteger(item.Release.ID); parseErr == nil {
					attributes = append(attributes, "release_id", releaseID)
				}
				client.logger.WarnContext(ctx, "dropping invalid AniLiberty torrent", attributes...)
			}
			continue
		}
		torrents = append(torrents, torrent)
	}
	if invalidCount > invalidTorrentLogSampleLimit {
		client.logger.WarnContext(ctx, "additional invalid AniLiberty torrents dropped",
			"operation", operation,
			"invalid_count", invalidCount,
			"sampled_count", invalidTorrentLogSampleLimit,
			"omitted_count", invalidCount-invalidTorrentLogSampleLimit,
		)
	}
	return torrents, nil
}

func (client *Client) getJSON(ctx context.Context, operation, requestURL string, decode func(context.Context, []byte) error) error {
	started := time.Now()
	for attempt := 1; attempt <= maximumAttempts; attempt++ {
		if err := client.acquire(ctx); err != nil {
			return newError(ErrorClassCanceled, operation, 0, attempt-1, started, err)
		}
		attemptStarted, err := client.limiter.wait(ctx)
		if err != nil {
			client.release()
			return newError(ErrorClassCanceled, operation, 0, attempt-1, started, err)
		}
		if client.attemptStarted != nil {
			client.attemptStarted(attemptStarted)
		}

		status, retryAfter, body, err := client.attempt(ctx, requestURL)
		client.release()
		if err != nil {
			class := ErrorClassTransport
			cause := err
			if errors.Is(err, errResponseTooLarge) {
				class = ErrorClassResponse
			} else if ctx.Err() != nil {
				class = ErrorClassCanceled
				cause = ctx.Err()
			}
			return newError(class, operation, status, attempt, started, cause)
		}
		if err := ctx.Err(); err != nil {
			return newError(ErrorClassCanceled, operation, status, attempt, started, err)
		}
		if status >= 200 && status <= 299 {
			decodeErr := decode(ctx, body)
			if err := ctx.Err(); err != nil {
				return newError(ErrorClassCanceled, operation, status, attempt, started, err)
			}
			if decodeErr != nil {
				class := ErrorClassResponse
				if errors.Is(decodeErr, context.Canceled) || errors.Is(decodeErr, context.DeadlineExceeded) {
					class = ErrorClassCanceled
				}
				return newError(class, operation, status, attempt, started, decodeErr)
			}
			return nil
		}

		class, retry := classifyStatus(status)
		if !retry || attempt == maximumAttempts {
			return newError(class, operation, status, attempt, started, nil)
		}
		delay, validRetryAfter := parseRetryAfter(retryAfter, time.Now())
		if !validRetryAfter {
			delay = client.backoff(attempt)
		}
		if err := waitForRetry(ctx, delay); err != nil {
			return newError(ErrorClassCanceled, operation, status, attempt, started, err)
		}
	}
	panic("unreachable")
}

func (client *Client) attempt(ctx context.Context, requestURL string) (int, string, []byte, error) {
	attemptContext, cancel := context.WithTimeout(ctx, client.httpTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(attemptContext, http.MethodGet, requestURL, nil)
	if err != nil {
		return 0, "", nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", client.userAgent)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return 0, "", nil, err
	}
	defer response.Body.Close()

	retryAfter := response.Header.Get("Retry-After")
	if response.StatusCode < 200 || response.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 32<<10))
		return response.StatusCode, retryAfter, nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, client.maxResponseBytes+1))
	if err != nil {
		return 0, "", nil, err
	}
	if int64(len(body)) > client.maxResponseBytes {
		return response.StatusCode, "", nil, errResponseTooLarge
	}
	if err := attemptContext.Err(); err != nil {
		return 0, "", nil, err
	}
	return response.StatusCode, "", body, nil
}

func (client *Client) endpoint(path string, query url.Values) string {
	reference := &url.URL{Path: path, RawQuery: query.Encode()}
	return client.baseURL.ResolveReference(reference).String()
}

func (client *Client) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case client.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-client.gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (client *Client) release() {
	<-client.gate
}

func (limiter *intervalLimiter) wait(ctx context.Context) (time.Time, error) {
	for {
		if err := ctx.Err(); err != nil {
			return time.Time{}, err
		}
		limiter.mu.Lock()
		now := time.Now()
		if !now.Before(limiter.next) {
			limiter.next = now.Add(limiter.interval)
			limiter.mu.Unlock()
			return now, nil
		}
		delay := time.Until(limiter.next)
		limiter.mu.Unlock()
		if err := waitContext(ctx, delay); err != nil {
			return time.Time{}, err
		}
	}
}

func (client *Client) backoff(attempt int) time.Duration {
	ceiling := backoffBase << (attempt - 1)
	if ceiling > backoffMaximum {
		ceiling = backoffMaximum
	}
	floor := ceiling / 2
	return floor + time.Duration(client.randomFloat()*float64(ceiling-floor))
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0, true
		}
		const maximumDuration = time.Duration(1<<63 - 1)
		if seconds > int64(maximumDuration/time.Second) {
			return maximumDuration, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	if negativeDecimal(value) {
		return 0, true
	}
	date, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	delay := date.Sub(now)
	if delay < 0 {
		delay = 0
	}
	return delay, true
}

func negativeDecimal(value string) bool {
	if len(value) < 2 || value[0] != '-' {
		return false
	}
	for index := 1; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	deadline, bounded := ctx.Deadline()
	if provider, ok := ctx.(interface {
		EffectiveDeadline() (time.Time, bool)
	}); ok {
		deadline, bounded = provider.EffectiveDeadline()
	}
	if bounded && time.Until(deadline) < delay {
		return context.DeadlineExceeded
	}
	return waitContext(ctx, delay)
}

func waitContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func classifyStatus(status int) (ErrorClass, bool) {
	switch status {
	case http.StatusTooManyRequests:
		return ErrorClassRateLimited, true
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return ErrorClassTemporary, true
	default:
		if status >= 400 && status <= 499 {
			return ErrorClassPermanent, false
		}
		return ErrorClassUnexpected, false
	}
}

func decodeTorrentCollection(ctx context.Context, body []byte, destination *[]rawTorrent) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return fmt.Errorf("empty JSON response")
	}
	switch trimmed[0] {
	case '[':
		return decodeJSON(ctx, trimmed, destination)
	case '{':
		var singleton rawTorrent
		if err := decodeJSON(ctx, trimmed, &singleton); err != nil {
			return err
		}
		*destination = []rawTorrent{singleton}
		return nil
	default:
		return fmt.Errorf("torrent response must be an array or object")
	}
}

func decodeJSON(ctx context.Context, body []byte, destination any) error {
	decoder := json.NewDecoder(contextReader{ctx: ctx, reader: bytes.NewReader(body)})
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	const maximumChunk = 32 << 10
	if len(buffer) > maximumChunk {
		buffer = buffer[:maximumChunk]
	}
	return reader.reader.Read(buffer)
}

func (client *Client) responseError(operation string, cause error) *Error {
	return newError(ErrorClassResponse, operation, 0, 0, time.Now(), cause)
}

var errResponseTooLarge = errors.New("decompressed response exceeds configured limit")
