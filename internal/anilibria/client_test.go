package anilibria

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientRequestsAndDecodesEndpointShapes(t *testing.T) {
	t.Parallel()

	searchBody := fixtureString(t, "search_multiple.json")
	torrentsBody := fixtureString(t, "torrents_array.json")
	latestBody := fixtureString(t, "latest.json")
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if got := request.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		if got := request.Header.Get("User-Agent"); got != "anilibria-torznab/v0.1.0 (+https://github.com/Zenderg/anilibria-torznab)" {
			t.Errorf("User-Agent = %q", got)
		}
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/app/search/releases":
			if got := request.URL.Query().Get("query"); got != "A&B Наруто" {
				t.Errorf("query = %q", got)
			}
			if got := request.URL.Query().Get("include"); got != "id" {
				t.Errorf("include = %q", got)
			}
			_, _ = io.WriteString(response, searchBody)
		case "/api/v1/anime/torrents/release/413":
			assertTorrentInclude(t, request)
			_, _ = io.WriteString(response, torrentsBody)
		case "/api/v1/anime/torrents":
			assertTorrentInclude(t, request)
			if got := request.URL.Query().Get("limit"); got != "50" {
				t.Errorf("limit = %q", got)
			}
			_, _ = io.WriteString(response, latestBody)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL+"/api/v1/", nil)
	client.userAgent = "anilibria-torznab/v0.1.0 (+https://github.com/Zenderg/anilibria-torznab)"

	ids, err := client.SearchReleases(context.Background(), "A&B Наруто")
	if err != nil {
		t.Fatalf("SearchReleases: %v", err)
	}
	if !slices.Equal(ids, []ReleaseID{413, 2495, 3996}) {
		t.Fatalf("release IDs = %v", ids)
	}

	torrents, err := client.TorrentsByRelease(context.Background(), 413)
	if err != nil {
		t.Fatalf("TorrentsByRelease: %v", err)
	}
	assertValidTorrent(t, torrents)

	latest, err := client.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	assertValidTorrent(t, latest)
	if requests.Load() != 3 {
		t.Fatalf("requests = %d", requests.Load())
	}
}

func TestClientPreservesEncodedSlashInBasePath(t *testing.T) {
	t.Parallel()

	paths := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		paths <- request.URL.EscapedPath()
		_, _ = io.WriteString(response, `[]`)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL+"/proxy%2Ftenant/api/v1", nil)
	if _, err := client.SearchReleases(context.Background(), "query"); err != nil {
		t.Fatal(err)
	}
	if got := <-paths; got != "/proxy%2Ftenant/api/v1/app/search/releases" {
		t.Fatalf("escaped request path = %q", got)
	}
}

func TestTorrentsByReleaseAcceptsArrayAndSingleton(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		fixture string
	}{
		{"array", "torrents_array.json"},
		{"singleton", "torrents_singleton.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := jsonServer(fixtureString(t, test.fixture))
			defer server.Close()
			client := newTestClient(t, server.URL+"/", nil)
			torrents, err := client.TorrentsByRelease(context.Background(), 413)
			if err != nil {
				t.Fatalf("TorrentsByRelease: %v", err)
			}
			assertValidTorrent(t, torrents)
		})
	}
}

func TestInvalidTorrentIsOmittedAndLoggedSafely(t *testing.T) {
	t.Parallel()

	const secretLabel = "PRIVATE-LABEL"
	const secretMagnet = "magnet:?xt=urn:btih:PRIVATE"
	invalid := strings.ReplaceAll(validTorrentJSON(testHash), `"label":"1080p"`, `"label":"`+secretLabel+`"`)
	invalid = strings.ReplaceAll(invalid, `"magnet":"magnet:?xt=urn:btih:`+testHash+`"`, `"magnet":"`+secretMagnet+`"`)
	invalid = strings.ReplaceAll(invalid, `"size":123456`, `"size":-1`)
	body := "[" + invalid + "," + validTorrentJSON(testHash) + "]"
	server := jsonServer(body)
	defer server.Close()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	client := newTestClient(t, server.URL+"/", logger)
	torrents, err := client.TorrentsByRelease(context.Background(), 413)
	if err != nil {
		t.Fatalf("TorrentsByRelease: %v", err)
	}
	if len(torrents) != 1 {
		t.Fatalf("torrent count = %d", len(torrents))
	}
	logOutput := logs.String()
	for _, forbidden := range []string{secretLabel, secretMagnet, testHash} {
		if strings.Contains(logOutput, forbidden) {
			t.Fatalf("log disclosed %q: %s", forbidden, logOutput)
		}
	}
	for _, required := range []string{"field=size", "item_index=0", "release_id=413"} {
		if !strings.Contains(logOutput, required) {
			t.Errorf("log missing %q: %s", required, logOutput)
		}
	}
}

func TestInvalidTorrentWarningsAreBounded(t *testing.T) {
	t.Parallel()

	const invalidItems = 10_000
	var logs bytes.Buffer
	client := &Client{logger: slog.New(slog.NewTextHandler(&logs, nil))}
	torrents, err := client.validTorrents(context.Background(), "latest", make([]rawTorrent, invalidItems), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(torrents) != 0 {
		t.Fatalf("torrent count = %d", len(torrents))
	}

	logOutput := logs.String()
	if warnings := strings.Count(logOutput, "level=WARN"); warnings != invalidTorrentLogSampleLimit+1 {
		t.Fatalf("warning count = %d, want %d", warnings, invalidTorrentLogSampleLimit+1)
	}
	for _, required := range []string{
		"invalid_count=10000",
		"sampled_count=5",
		"omitted_count=9995",
	} {
		if !strings.Contains(logOutput, required) {
			t.Errorf("aggregate log missing %q: %s", required, logOutput)
		}
	}
}

func TestInvalidSourceStringEncodingIsOmittedPerTorrent(t *testing.T) {
	t.Parallel()

	fields := []struct {
		name   string
		target string
		key    string
	}{
		{"hash", `"hash":"` + testHash + `"`, `"hash":`},
		{"label", `"label":"1080p"`, `"label":`},
		{"magnet", `"magnet":"magnet:?xt=urn:btih:` + testHash + `"`, `"magnet":`},
		{"updated_at", `"updated_at":"2026-07-16T10:11:12Z"`, `"updated_at":`},
		{"release.type.value", `"value":"TV"`, `"value":`},
		{"release.name.main", `"main":"Naruto"`, `"main":`},
		{"release.alias", `"alias":"naruto"`, `"alias":`},
	}
	encodings := []struct {
		name    string
		literal string
	}{
		{"invalid UTF-8", `"bad` + string([]byte{0xff}) + `value"`},
		{"lone surrogate", `"bad\uD800value"`},
	}
	for _, encoding := range encodings {
		encoding := encoding
		for _, field := range fields {
			field := field
			t.Run(encoding.name+"/"+field.name, func(t *testing.T) {
				t.Parallel()
				invalid := strings.Replace(validTorrentJSON(testHash), field.target, field.key+encoding.literal, 1)
				if invalid == validTorrentJSON(testHash) {
					t.Fatalf("test did not replace %s", field.name)
				}
				server := jsonServer("[" + invalid + "," + validTorrentJSON(testHash) + "]")
				defer server.Close()

				var logs bytes.Buffer
				client := newTestClient(t, server.URL+"/", slog.New(slog.NewTextHandler(&logs, nil)))
				torrents, err := client.TorrentsByRelease(context.Background(), 413)
				if err != nil {
					t.Fatalf("TorrentsByRelease: %v", err)
				}
				if len(torrents) != 1 {
					t.Fatalf("torrent count = %d, want valid sibling only", len(torrents))
				}
				if !strings.Contains(logs.String(), "field="+field.name) {
					t.Fatalf("validation log does not identify %s: %s", field.name, logs.String())
				}
			})
		}
	}
}

func TestTorrentsByReleaseOmitsMismatchedReleaseAssociation(t *testing.T) {
	t.Parallel()

	mismatched := strings.Replace(validTorrentJSON(testHash), `"id":413`, `"id":999`, 1)
	server := jsonServer("[" + mismatched + "," + validTorrentJSON(testHash) + "]")
	defer server.Close()
	var logs bytes.Buffer
	client := newTestClient(t, server.URL+"/", slog.New(slog.NewTextHandler(&logs, nil)))
	torrents, err := client.TorrentsByRelease(context.Background(), 413)
	if err != nil {
		t.Fatalf("TorrentsByRelease: %v", err)
	}
	if len(torrents) != 1 || torrents[0].Release.ID != 413 {
		t.Fatalf("torrents = %+v, want requested release only", torrents)
	}
	if logOutput := logs.String(); !strings.Contains(logOutput, "field=release.id") || !strings.Contains(logOutput, "release_id=999") {
		t.Fatalf("mismatch validation log = %s", logOutput)
	}
}

func TestLatestKeepsReleaseAssociationValidationIndependent(t *testing.T) {
	t.Parallel()

	otherRelease := strings.Replace(validTorrentJSON(testHash), `"id":413`, `"id":999`, 1)
	server := jsonServer(`{"data":[` + otherRelease + `]}`)
	defer server.Close()
	client := newTestClient(t, server.URL+"/", nil)
	torrents, err := client.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(torrents) != 1 || torrents[0].Release.ID != 999 {
		t.Fatalf("latest torrents = %+v", torrents)
	}
}

func TestStatusClassificationAndRetryEligibility(t *testing.T) {
	t.Parallel()

	validationBody := fixtureString(t, "validation_error.json")
	temporaryBody := fixtureString(t, "temporary_error.json")
	tests := []struct {
		status   int
		class    ErrorClass
		attempts int
	}{
		{http.StatusTooManyRequests, ErrorClassRateLimited, 3},
		{http.StatusInternalServerError, ErrorClassTemporary, 3},
		{http.StatusBadGateway, ErrorClassTemporary, 3},
		{http.StatusServiceUnavailable, ErrorClassTemporary, 3},
		{http.StatusGatewayTimeout, ErrorClassTemporary, 3},
		{http.StatusBadRequest, ErrorClassPermanent, 1},
		{http.StatusNotFound, ErrorClassPermanent, 1},
		{http.StatusUnprocessableEntity, ErrorClassPermanent, 1},
		{http.StatusFound, ErrorClassUnexpected, 1},
		{http.StatusNotImplemented, ErrorClassUnexpected, 1},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("status_%d", test.status), func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				response.Header().Set("Retry-After", "0")
				if test.status == http.StatusFound {
					response.Header().Set("Location", "/must-not-follow")
				}
				response.WriteHeader(test.status)
				if test.attempts == maximumAttempts {
					_, _ = io.WriteString(response, temporaryBody)
				} else {
					_, _ = io.WriteString(response, validationBody)
				}
			}))
			defer server.Close()
			client := newTestClient(t, server.URL+"/", nil)
			_, err := client.SearchReleases(context.Background(), "query")
			assertClientError(t, err, test.class, test.status, test.attempts)
			if got := int(calls.Load()); got != test.attempts {
				t.Fatalf("calls = %d, want %d", got, test.attempts)
			}
		})
	}
}

func TestRetryAttemptUsesSharedStartIntervalAndCanSucceed(t *testing.T) {
	t.Parallel()

	const interval = 25 * time.Millisecond
	var mu sync.Mutex
	var starts []time.Time
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		attempt := calls.Add(1)
		if attempt == 1 {
			response.Header().Set("Retry-After", "0")
			response.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(response, `[{"id":413}]`)
	}))
	defer server.Close()
	client := newTestClientWithOptions(t, server.URL+"/", nil, 1<<20, interval, 1, time.Second)
	client.attemptStarted = func(started time.Time) {
		mu.Lock()
		starts = append(starts, started)
		mu.Unlock()
	}
	ids, err := client.SearchReleases(context.Background(), "query")
	if err != nil {
		t.Fatalf("SearchReleases: %v", err)
	}
	if !slices.Equal(ids, []ReleaseID{413}) {
		t.Fatalf("release IDs = %v", ids)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls.Load() != 2 || len(starts) != 2 || starts[1].Sub(starts[0]) < interval {
		t.Fatalf("retry starts = %v", starts)
	}
}

func TestInvalidJSONAndRequiredDataAreNotRetried(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		body string
	}{
		{"invalid JSON", `{"broken"`},
		{"wrong top level", `{"id":413}`},
		{"null top level", `null`},
		{"fractional ID", `[{"id":413.5}]`},
		{"trailing JSON", `[{"id":413}] []`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				_, _ = io.WriteString(response, test.body)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL+"/", nil)
			_, err := client.SearchReleases(context.Background(), "query")
			assertClientError(t, err, ErrorClassResponse, http.StatusOK, 1)
			if calls.Load() != 1 {
				t.Fatalf("calls = %d", calls.Load())
			}
		})
	}
}

func TestReleaseIDsAcceptIntegerValuedJSONNumbers(t *testing.T) {
	t.Parallel()

	server := jsonServer(`[{"id":413.0},{"id":2.495e3}]`)
	defer server.Close()
	client := newTestClient(t, server.URL+"/", nil)
	ids, err := client.SearchReleases(context.Background(), "query")
	if err != nil {
		t.Fatalf("SearchReleases: %v", err)
	}
	if !slices.Equal(ids, []ReleaseID{413, 2495}) {
		t.Fatalf("release IDs = %v", ids)
	}
}

func TestEmptySearchFixture(t *testing.T) {
	t.Parallel()

	server := jsonServer(fixtureString(t, "search_empty.json"))
	defer server.Close()
	client := newTestClient(t, server.URL+"/", nil)
	ids, err := client.SearchReleases(context.Background(), "no match")
	if err != nil {
		t.Fatalf("SearchReleases: %v", err)
	}
	if ids == nil || len(ids) != 0 {
		t.Fatalf("release IDs = %#v, want non-nil empty slice", ids)
	}
}

func TestDecompressedResponseLimitIsPermanent(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		response.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(response)
		_, _ = io.WriteString(writer, strings.Repeat(" ", 1024)+`[]`)
		_ = writer.Close()
	}))
	defer server.Close()
	client := newTestClientWithOptions(t, server.URL+"/", nil, 128, time.Microsecond, 2, 500*time.Millisecond)

	_, err := client.SearchReleases(context.Background(), "query")
	assertClientError(t, err, ErrorClassResponse, http.StatusOK, 1)
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
	if strings.Contains(err.Error(), server.URL) {
		t.Fatalf("error disclosed URL: %v", err)
	}
}

func TestTransportFailureAndAttemptTimeoutAreNotRetried(t *testing.T) {
	t.Parallel()

	t.Run("transport", func(t *testing.T) {
		t.Parallel()
		const sensitiveURL = "https://upstream.invalid/?query=secret"
		transportErr := errors.New("dial failed for " + sensitiveURL)
		var calls atomic.Int64
		httpClient := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, transportErr
		})}
		client := newTestClientWithHTTPClient(t, "https://upstream.invalid/api/v1/", httpClient, 100*time.Millisecond)
		_, err := client.SearchReleases(context.Background(), "secret")
		assertClientError(t, err, ErrorClassTransport, 0, 1)
		if calls.Load() != 1 {
			t.Fatalf("calls = %d", calls.Load())
		}
		if strings.Contains(err.Error(), sensitiveURL) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("printable error disclosed request data: %v", err)
		}
		if !errors.Is(err, transportErr) {
			t.Fatalf("wrapped cause not retained")
		}
	})

	t.Run("attempt timeout", func(t *testing.T) {
		t.Parallel()
		var calls atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			calls.Add(1)
			<-request.Context().Done()
		}))
		defer server.Close()
		client := newTestClientWithOptions(t, server.URL+"/", nil, 1<<20, time.Microsecond, 1, 20*time.Millisecond)
		_, err := client.SearchReleases(context.Background(), "query")
		assertClientError(t, err, ErrorClassTransport, 0, 1)
		if calls.Load() != 1 {
			t.Fatalf("calls = %d", calls.Load())
		}
	})
}

func TestRetryAfterThatExceedsDeadlineDoesNotSleepOrRetry(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		response.Header().Set("Retry-After", "1")
		response.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL+"/", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := client.SearchReleases(ctx, "query")
	assertClientError(t, err, ErrorClassCanceled, http.StatusServiceUnavailable, 1)
	if elapsed := time.Since(started); elapsed >= 80*time.Millisecond {
		t.Fatalf("operation slept despite insufficient deadline budget: %v", elapsed)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error does not unwrap to deadline exceeded: %v", err)
	}
}

func TestSharedAttemptStartIntervalAcrossOperations(t *testing.T) {
	t.Parallel()

	const interval = 30 * time.Millisecond
	var mu sync.Mutex
	var starts []time.Time
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case strings.Contains(request.URL.Path, "/app/search/releases"):
			_, _ = io.WriteString(response, `[]`)
		case strings.Contains(request.URL.Path, "/anime/torrents/release/"):
			_, _ = io.WriteString(response, `[]`)
		default:
			_, _ = io.WriteString(response, `{"data":[]}`)
		}
	}))
	defer server.Close()
	client := newTestClientWithOptions(t, server.URL+"/", nil, 1<<20, interval, 3, time.Second)
	client.attemptStarted = func(started time.Time) {
		mu.Lock()
		starts = append(starts, started)
		mu.Unlock()
	}

	var group sync.WaitGroup
	group.Add(3)
	go func() { defer group.Done(); _, _ = client.SearchReleases(context.Background(), "q") }()
	go func() { defer group.Done(); _, _ = client.TorrentsByRelease(context.Background(), 1) }()
	go func() { defer group.Done(); _, _ = client.Latest(context.Background()) }()
	group.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 3 {
		t.Fatalf("attempt starts = %d", len(starts))
	}
	slices.SortFunc(starts, time.Time.Compare)
	for index := 1; index < len(starts); index++ {
		if spacing := starts[index].Sub(starts[index-1]); spacing < interval {
			t.Fatalf("limiter reservation spacing %d = %v, want at least %v", index, spacing, interval)
		}
	}
}

func TestSharedConcurrencyGateBoundsInFlightAttempts(t *testing.T) {
	t.Parallel()

	var current atomic.Int64
	var maximum atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		active := current.Add(1)
		for {
			observed := maximum.Load()
			if active <= observed || maximum.CompareAndSwap(observed, active) {
				break
			}
		}
		time.Sleep(35 * time.Millisecond)
		current.Add(-1)
		_, _ = io.WriteString(response, `[]`)
	}))
	defer server.Close()
	client := newTestClientWithOptions(t, server.URL+"/", nil, 1<<20, time.Microsecond, 2, time.Second)

	var group sync.WaitGroup
	group.Add(6)
	for index := 0; index < 6; index++ {
		go func() {
			defer group.Done()
			_, _ = client.SearchReleases(context.Background(), "query")
		}()
	}
	group.Wait()
	if got := maximum.Load(); got != 2 {
		t.Fatalf("maximum in-flight attempts = %d, want 2", got)
	}
}

func TestCancellationWhileWaitingForLimiterStartsNoAttempt(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(response, `[]`)
	}))
	defer server.Close()
	client := newTestClientWithOptions(t, server.URL+"/", nil, 1<<20, 200*time.Millisecond, 2, time.Second)
	if _, err := client.SearchReleases(context.Background(), "first"); err != nil {
		t.Fatalf("first request: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := client.SearchReleases(ctx, "second")
	assertClientError(t, err, ErrorClassCanceled, 0, 0)
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestCancellationWhileWaitingForConcurrencyGateStartsNoAttempt(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		_, _ = io.WriteString(response, `[]`)
	}))
	defer server.Close()
	client := newTestClientWithOptions(t, server.URL+"/", nil, 1<<20, time.Microsecond, 1, time.Second)

	firstDone := make(chan error, 1)
	go func() {
		_, err := client.SearchReleases(context.Background(), "first")
		firstDone <- err
	}()
	<-firstStarted
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := client.SearchReleases(ctx, "second")
	assertClientError(t, err, ErrorClassCanceled, 0, 0)
	if calls.Load() != 1 {
		t.Fatalf("calls before releasing gate = %d", calls.Load())
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first request: %v", err)
	}
}

func TestParentCancellationStopsHTTPAttempt(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		close(requestStarted)
		<-request.Context().Done()
	}))
	defer server.Close()
	client := newTestClientWithOptions(t, server.URL+"/", nil, 1<<20, time.Microsecond, 1, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.SearchReleases(ctx, "query")
		done <- err
	}()
	<-requestStarted
	cancel()
	err := <-done
	assertClientError(t, err, ErrorClassCanceled, 0, 1)
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("cancellation result err=%v calls=%d", err, calls.Load())
	}
}

func TestCancellationAfterBodyReadStopsJSONDecode(t *testing.T) {
	t.Parallel()

	server := jsonServer(`{"value":1}`)
	defer server.Close()
	client := newTestClient(t, server.URL+"/", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bodyRead := make(chan struct{})
	continueDecode := make(chan struct{})
	var decoded atomic.Bool
	done := make(chan error, 1)
	go func() {
		done <- client.getJSON(ctx, "decode_cancellation", server.URL+"/", func(decodeContext context.Context, body []byte) error {
			close(bodyRead)
			<-continueDecode
			return decodeJSON(decodeContext, body, &decodeProbe{called: &decoded})
		})
	}()

	<-bodyRead
	cancel()
	close(continueDecode)
	err := <-done
	assertClientError(t, err, ErrorClassCanceled, http.StatusOK, 1)
	if decoded.Load() {
		t.Fatal("JSON unmarshaler ran after the parent context was canceled")
	}
}

func TestNegativeRetryAfterDoesNotUseLocalBackoff(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		response.Header().Set("Retry-After", "-1")
		response.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL+"/", nil)
	var backoffCalled atomic.Bool
	client.randomFloat = func() float64 {
		backoffCalled.Store(true)
		return 0
	}

	_, err := client.SearchReleases(context.Background(), "query")
	assertClientError(t, err, ErrorClassTemporary, http.StatusServiceUnavailable, maximumAttempts)
	if calls.Load() != maximumAttempts {
		t.Fatalf("calls = %d, want %d", calls.Load(), maximumAttempts)
	}
	if backoffCalled.Load() {
		t.Fatal("negative Retry-After used local jittered backoff")
	}
}

func TestRetryAfterParsingAndBoundedJitter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name  string
		value string
		want  time.Duration
		valid bool
	}{
		{"delta seconds", "12", 12 * time.Second, true},
		{"zero", "0", 0, true},
		{"HTTP date", now.Add(8 * time.Second).Format(http.TimeFormat), 8 * time.Second, true},
		{"expired HTTP date", now.Add(-time.Second).Format(http.TimeFormat), 0, true},
		{"negative", "-1", 0, true},
		{"negative overflow", "-999999999999999999999999999999", 0, true},
		{"malformed", "private-value", 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, valid := parseRetryAfter(test.value, now)
			if got != test.want || valid != test.valid {
				t.Fatalf("parseRetryAfter = (%v, %v), want (%v, %v)", got, valid, test.want, test.valid)
			}
		})
	}

	client := &Client{randomFloat: func() float64 { return 0 }}
	if got := client.backoff(1); got != backoffBase/2 {
		t.Fatalf("minimum backoff = %v", got)
	}
	client.randomFloat = func() float64 { return 0.999999 }
	if got := client.backoff(20); got < backoffMaximum/2 || got > backoffMaximum {
		t.Fatalf("bounded backoff = %v", got)
	}
}

func TestNewClientValidation(t *testing.T) {
	t.Parallel()

	valid := Config{
		BaseURL:          "https://example.test/api/v1/",
		HTTPTimeout:      time.Second,
		RequestInterval:  time.Millisecond,
		MaxConcurrency:   1,
		MaxResponseBytes: 1024,
	}
	for _, mutate := range []func(*Config){
		func(config *Config) { config.BaseURL = "://bad" },
		func(config *Config) { config.BaseURL = "ftp://example.test/" },
		func(config *Config) { config.BaseURL = "https://user@example.test/" },
		func(config *Config) { config.BaseURL = "https://example.test/?secret=x" },
		func(config *Config) { config.HTTPTimeout = 0 },
		func(config *Config) { config.RequestInterval = 0 },
		func(config *Config) { config.MaxConcurrency = 0 },
		func(config *Config) { config.MaxResponseBytes = 0 },
		func(config *Config) { config.Version = "bad version" },
	} {
		config := valid
		mutate(&config)
		if _, err := NewClient(config); err == nil {
			t.Fatalf("NewClient(%+v) unexpectedly succeeded", config)
		}
	}
}

func TestSearchRejectsBlankQueryWithoutUpstreamCall(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL+"/", nil)
	_, err := client.SearchReleases(context.Background(), " \t\n")
	assertClientError(t, err, ErrorClassResponse, 0, 0)
	if calls.Load() != 0 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestLatestRequiresArrayData(t *testing.T) {
	t.Parallel()

	for _, body := range []string{`{}`, `{"data":null}`, `{"data":{}}`} {
		server := jsonServer(body)
		client := newTestClient(t, server.URL+"/", nil)
		_, err := client.Latest(context.Background())
		server.Close()
		assertClientError(t, err, ErrorClassResponse, http.StatusOK, 1)
	}
}

func assertTorrentInclude(t *testing.T, request *http.Request) {
	t.Helper()
	values, ok := request.URL.Query()["include"]
	if !ok || len(values) != 1 || values[0] != torrentInclude {
		t.Errorf("include values = %v", values)
	}
}

func assertValidTorrent(t *testing.T, torrents []Torrent) {
	t.Helper()
	if len(torrents) != 1 {
		t.Fatalf("torrent count = %d", len(torrents))
	}
	torrent := torrents[0]
	if torrent.Hash != testHash || torrent.Size != 123456 || torrent.Seeders != 10 || torrent.Leechers != 2 || torrent.CompletedTimes != 99 {
		t.Fatalf("torrent values = %+v", torrent)
	}
	if torrent.Release.ID != 413 || torrent.Release.Type != ReleaseTypeTV || torrent.Release.Year != 2002 || torrent.Release.MainName != "Naruto" || torrent.Release.Alias != "naruto" {
		t.Fatalf("release values = %+v", torrent.Release)
	}
	wantTime := time.Date(2026, 7, 16, 10, 11, 12, 0, time.UTC)
	if !torrent.UpdatedAt.Equal(wantTime) {
		t.Fatalf("UpdatedAt = %v", torrent.UpdatedAt)
	}
}

func assertClientError(t *testing.T, err error, class ErrorClass, status, attempts int) *Error {
	t.Helper()
	var clientError *Error
	if !errors.As(err, &clientError) {
		t.Fatalf("error = %T %v, want *Error", err, err)
	}
	if clientError.Class != class || clientError.StatusCode != status || clientError.Attempts != attempts {
		t.Fatalf("error = %+v, want class=%s status=%d attempts=%d", clientError, class, status, attempts)
	}
	if clientError.Duration < 0 {
		t.Fatalf("negative duration: %v", clientError.Duration)
	}
	return clientError
}

func newTestClient(t *testing.T, baseURL string, logger *slog.Logger) *Client {
	t.Helper()
	return newTestClientWithOptions(t, baseURL, logger, 1<<20, time.Microsecond, 4, time.Second)
}

func newTestClientWithOptions(t *testing.T, baseURL string, logger *slog.Logger, maxBytes int64, interval time.Duration, concurrency int, timeout time.Duration) *Client {
	t.Helper()
	client, err := NewClient(Config{
		BaseURL:          baseURL,
		Version:          "dev",
		HTTPTimeout:      timeout,
		RequestInterval:  interval,
		MaxConcurrency:   concurrency,
		MaxResponseBytes: maxBytes,
		Logger:           logger,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func newTestClientWithHTTPClient(t *testing.T, baseURL string, httpClient *http.Client, timeout time.Duration) *Client {
	t.Helper()
	client, err := NewClient(Config{
		BaseURL:          baseURL,
		Version:          "dev",
		HTTPClient:       httpClient,
		HTTPTimeout:      timeout,
		RequestInterval:  time.Microsecond,
		MaxConcurrency:   1,
		MaxResponseBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func jsonServer(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, body)
	}))
}

func fixtureString(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(body)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type decodeProbe struct {
	called *atomic.Bool
}

func (probe *decodeProbe) UnmarshalJSON([]byte) error {
	probe.called.Store(true)
	return nil
}

const testHash = "0123456789abcdef0123456789abcdef01234567"

func validTorrentJSON(hash string) string {
	return fmt.Sprintf(`{
		"hash":%q,
		"size":123456,
		"label":"1080p",
		"magnet":%q,
		"seeders":10,
		"leechers":2,
		"completed_times":99,
		"updated_at":"2026-07-16T10:11:12Z",
		"release":{
			"id":413,
			"type":{"value":"TV"},
			"year":2002,
			"name":{"main":"Naruto"},
			"alias":"naruto"
		}
	}`, hash, "magnet:?xt=urn:btih:"+hash)
}
