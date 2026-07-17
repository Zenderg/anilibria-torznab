package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Zenderg/anilibria-torznab/internal/anilibria"
	"github.com/Zenderg/anilibria-torznab/internal/service"
	"github.com/Zenderg/anilibria-torznab/internal/torznab"
)

func TestRealClientServiceFanoutProtection(t *testing.T) {
	t.Run("spaces all attempts, bounds concurrency, and returns deterministic results", func(t *testing.T) {
		const (
			requestInterval = 12 * time.Millisecond
			maxConcurrency  = 3
		)
		probe := &fanoutProbe{}
		upstream := newFanoutUpstream(t, probe, 50*time.Millisecond)
		defer upstream.Close()
		searchService := newIntegratedService(t, upstream.URL, requestInterval, maxConcurrency)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		response, err := searchService.Execute(ctx, integratedRequest(t))
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}

		starts, maximum, releaseCalls := probe.snapshot()
		if len(starts) != 11 || releaseCalls != 10 {
			t.Fatalf("upstream attempts: total=%d release=%d, want 11 and 10", len(starts), releaseCalls)
		}
		const schedulingTolerance = 4 * time.Millisecond
		for index := 1; index < len(starts); index++ {
			if spacing := starts[index].Sub(starts[index-1]); spacing < requestInterval-schedulingTolerance {
				t.Fatalf("attempt spacing %d = %v, want at least %v", index, spacing, requestInterval-schedulingTolerance)
			}
		}
		if maximum > maxConcurrency || maximum < 2 {
			t.Fatalf("maximum server in-flight = %d, want overlapping requests bounded by %d", maximum, maxConcurrency)
		}

		if response.Stats.ReleaseCount != 10 || response.Stats.FailedBranches != 0 || response.Stats.ResultCount != 10 {
			t.Fatalf("stats = %+v", response.Stats)
		}
		if response.Feed.Total != 10 || len(response.Feed.Items) != 10 {
			t.Fatalf("feed total/items = %d/%d", response.Feed.Total, len(response.Feed.Items))
		}
		for index, item := range response.Feed.Items {
			wantReleaseID := 10 - index
			if item.InfoHash != integrationHash(wantReleaseID) {
				t.Fatalf("item %d hash = %q, want release %d hash %q", index, item.InfoHash, wantReleaseID, integrationHash(wantReleaseID))
			}
		}
	})

	t.Run("parent deadline stops a limiter schedule before all attempts", func(t *testing.T) {
		const (
			requestInterval = 18 * time.Millisecond
			deadline        = 75 * time.Millisecond
		)
		probe := &fanoutProbe{}
		upstream := newFanoutUpstream(t, probe, 2*time.Millisecond)
		defer upstream.Close()
		searchService := newIntegratedService(t, upstream.URL, requestInterval, 4)

		ctx, cancel := context.WithTimeout(context.Background(), deadline)
		defer cancel()
		started := time.Now()
		_, err := searchService.Execute(ctx, integratedRequest(t))
		elapsed := time.Since(started)

		if ctx.Err() != context.DeadlineExceeded {
			t.Fatalf("parent context error = %v, want deadline exceeded", ctx.Err())
		}
		if elapsed > deadline+300*time.Millisecond {
			t.Fatalf("Execute() exceeded bounded cancellation time: %v", elapsed)
		}
		_, _, releaseCalls := probe.snapshot()
		if releaseCalls == 0 || releaseCalls >= 10 {
			t.Fatalf("release attempts before deadline = %d, want 1..9", releaseCalls)
		}
		if err == nil {
			t.Fatal("deadline returned a successful partial response")
		}
	})
}

func TestServicePreservesRetryBudgetThroughCoalescing(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		response.Header().Set("Retry-After", "10")
		response.WriteHeader(http.StatusTooManyRequests)
	}))
	defer upstream.Close()
	searchService := newIntegratedService(t, upstream.URL, time.Microsecond, 1)

	const deadline = 500 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	started := time.Now()
	_, err := searchService.Execute(ctx, integratedRequest(t))
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("Execute() unexpectedly succeeded")
	}
	if ctx.Err() != nil {
		t.Fatalf("request context expired before the budget error returned: %v", ctx.Err())
	}
	if elapsed >= deadline/2 {
		t.Fatalf("Execute() waited despite insufficient Retry-After budget: %v", elapsed)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls.Load())
	}
}

type fanoutProbe struct {
	mu           sync.Mutex
	starts       []time.Time
	inFlight     int
	maximum      int
	releaseCalls int
}

func (probe *fanoutProbe) begin(release bool) func() {
	probe.mu.Lock()
	probe.starts = append(probe.starts, time.Now())
	probe.inFlight++
	if probe.inFlight > probe.maximum {
		probe.maximum = probe.inFlight
	}
	if release {
		probe.releaseCalls++
	}
	probe.mu.Unlock()
	return func() {
		probe.mu.Lock()
		probe.inFlight--
		probe.mu.Unlock()
	}
}

func (probe *fanoutProbe) snapshot() ([]time.Time, int, int) {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	return append([]time.Time(nil), probe.starts...), probe.maximum, probe.releaseCalls
}

func newFanoutUpstream(t *testing.T, probe *fanoutProbe, releaseWork time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/api/v1/app/search/releases" {
			finish := probe.begin(false)
			defer finish()
			if request.URL.Query().Get("query") != "Fanout" || request.URL.Query().Get("include") != "id" {
				t.Errorf("unexpected search query: %q", request.URL.RawQuery)
			}
			ids := make([]map[string]int, 10)
			for index := range ids {
				ids[index] = map[string]int{"id": index + 1}
			}
			if err := json.NewEncoder(response).Encode(ids); err != nil {
				t.Errorf("encode search response: %v", err)
			}
			return
		}

		const releasePrefix = "/api/v1/anime/torrents/release/"
		if strings.HasPrefix(request.URL.Path, releasePrefix) {
			finish := probe.begin(true)
			defer finish()
			releaseID, err := strconv.Atoi(strings.TrimPrefix(request.URL.Path, releasePrefix))
			if err != nil || releaseID < 1 || releaseID > 10 {
				t.Errorf("unexpected release path: %q", request.URL.Path)
				response.WriteHeader(http.StatusBadRequest)
				return
			}
			timer := time.NewTimer(releaseWork)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-request.Context().Done():
				return
			}
			if err := json.NewEncoder(response).Encode([]any{integrationTorrent(releaseID)}); err != nil {
				t.Errorf("encode release response: %v", err)
			}
			return
		}

		http.NotFound(response, request)
	}))
}

func integrationTorrent(releaseID int) map[string]any {
	hash := integrationHash(releaseID)
	updatedAt := time.Date(2026, time.July, 16, 10, 0, 0, 0, time.UTC).Add(time.Duration(releaseID) * time.Minute)
	return map[string]any{
		"hash":            hash,
		"size":            1024 + releaseID,
		"label":           fmt.Sprintf("Fanout Release %d [01]", releaseID),
		"magnet":          "magnet:?xt=urn:btih:" + hash,
		"seeders":         releaseID,
		"leechers":        0,
		"completed_times": releaseID,
		"updated_at":      updatedAt.Format(time.RFC3339),
		"release": map[string]any{
			"id":    releaseID,
			"type":  map[string]any{"value": "TV"},
			"year":  2026,
			"name":  map[string]any{"main": fmt.Sprintf("Fanout %d", releaseID)},
			"alias": fmt.Sprintf("fanout-%d", releaseID),
		},
	}
}

func integrationHash(releaseID int) string {
	return fmt.Sprintf("%040x", releaseID)
}

func newIntegratedService(t *testing.T, upstreamBaseURL string, requestInterval time.Duration, maxConcurrency int) *service.Service {
	t.Helper()
	client, err := anilibria.NewClient(anilibria.Config{
		BaseURL:          upstreamBaseURL + "/api/v1/",
		Version:          "integration-test",
		HTTPTimeout:      time.Second,
		RequestInterval:  requestInterval,
		MaxConcurrency:   maxConcurrency,
		MaxResponseBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("anilibria.NewClient() error = %v", err)
	}
	searchService, err := service.New(client, service.Config{
		SiteBaseURL:          "https://aniliberty.example/",
		MaxReleasesPerSearch: 10,
		CacheMaxEntries:      32,
		SearchCacheTTL:       time.Minute,
		TorrentsCacheTTL:     time.Minute,
		LatestCacheTTL:       time.Minute,
		NegativeCacheTTL:     time.Minute,
	})
	if err != nil {
		t.Fatalf("service.New() error = %v", err)
	}
	return searchService
}

func integratedRequest(t *testing.T) service.Request {
	t.Helper()
	categories, err := torznab.ParseCategoryFilter(nil)
	if err != nil {
		t.Fatalf("ParseCategoryFilter(nil) error = %v", err)
	}
	return service.Request{
		Query:         "Fanout",
		QueryProvided: true,
		Categories:    categories,
		Limit:         50,
	}
}
