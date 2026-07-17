package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Zenderg/anilibria-torznab/internal/anilibria"
	"github.com/Zenderg/anilibria-torznab/internal/torznab"
)

func TestExecuteBlankSearchUsesLatestAndCachesEmptyWindow(t *testing.T) {
	client := &fakeClient{
		latestFn: func(context.Context) ([]anilibria.Torrent, error) {
			return []anilibria.Torrent{}, nil
		},
	}
	service := newTestService(t, client, nil)
	request := Request{Query: " \t\n", Categories: allCategories(t), Limit: 50}

	first, err := service.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	second, err := service.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}

	searches, torrents, latest := client.callSnapshot()
	if len(searches) != 0 || len(torrents) != 0 || latest != 1 {
		t.Fatalf("upstream calls: search=%v torrents=%v latest=%d", searches, torrents, latest)
	}
	if first.Feed.Total != 0 || len(first.Feed.Items) != 0 || first.Stats.CacheMisses != 1 {
		t.Errorf("first response = %+v", first)
	}
	if second.Feed.Total != 0 || len(second.Feed.Items) != 0 || second.Stats.CacheHits != 1 || second.Stats.CacheMisses != 0 {
		t.Errorf("cached response = %+v", second)
	}
}

func TestExecuteBlankTVSearchIsRejectedWithoutUpstreamIO(t *testing.T) {
	client := &fakeClient{}
	service := newTestService(t, client, nil)

	_, err := service.Execute(context.Background(), Request{
		Query:      "  ",
		TVSearch:   true,
		Categories: allCategories(t),
		Limit:      50,
	})
	assertServiceError(t, err, ErrorIncorrectParameter, "q")

	searches, torrents, latest := client.callSnapshot()
	if len(searches) != 0 || len(torrents) != 0 || latest != 0 {
		t.Fatalf("invalid request contacted upstream: search=%v torrents=%v latest=%d", searches, torrents, latest)
	}
}

func TestProcessBoundsInvalidTorrentWarnings(t *testing.T) {
	const invalidItems = 10_000
	tests := []struct {
		name          string
		prepare       func(*anilibria.Torrent)
		aggregateMark string
	}{
		{
			name: "invalid title",
			prepare: func(value *anilibria.Torrent) {
				value.Label = "[1080p]"
			},
			aggregateMark: "invalid_title_count=10000",
		},
		{
			name: "peer count overflow",
			prepare: func(value *anilibria.Torrent) {
				value.Seeders = math.MaxInt64
				value.Leechers = 1
			},
			aggregateMark: "peer_count_overflow_count=10000",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			torrents := make([]anilibria.Torrent, invalidItems)
			for index := range torrents {
				torrents[index] = torrent(anilibria.ReleaseID(index+1), anilibria.ReleaseTypeTV, fmt.Sprintf("%040x", index+1), "Example [01]", testTime(1))
				test.prepare(&torrents[index])
			}
			var logs strings.Builder
			searchService := newTestService(t, &fakeClient{}, func(config *Config) {
				config.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			})
			items, err := searchService.process(context.Background(), torrents, allCategories(t), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 0 {
				t.Fatalf("item count = %d", len(items))
			}
			logOutput := logs.String()
			if warnings := strings.Count(logOutput, "level=WARN"); warnings != processedTorrentLogSampleLimit+1 {
				t.Fatalf("warning count = %d, want %d", warnings, processedTorrentLogSampleLimit+1)
			}
			for _, required := range []string{"invalid_count=10000", "sampled_count=5", "omitted_count=9995", test.aggregateMark} {
				if !strings.Contains(logOutput, required) {
					t.Errorf("aggregate log missing %q: %s", required, logOutput)
				}
			}
			for _, forbidden := range []string{"[1080p]", "magnet:"} {
				if strings.Contains(logOutput, forbidden) {
					t.Errorf("log disclosed %q: %s", forbidden, logOutput)
				}
			}
		})
	}
}

func TestExecuteBoundsAndDeduplicatesFanoutIndependentOfCompletionOrder(t *testing.T) {
	const (
		idFirst  anilibria.ReleaseID = 3
		idSecond anilibria.ReleaseID = 1
		idThird  anilibria.ReleaseID = 2
	)
	started := map[anilibria.ReleaseID]chan struct{}{
		idFirst:  make(chan struct{}),
		idSecond: make(chan struct{}),
		idThird:  make(chan struct{}),
	}
	release := map[anilibria.ReleaseID]chan struct{}{
		idFirst:  make(chan struct{}),
		idSecond: make(chan struct{}),
		idThird:  make(chan struct{}),
	}
	finished := map[anilibria.ReleaseID]chan struct{}{
		idFirst:  make(chan struct{}, 1),
		idSecond: make(chan struct{}, 1),
		idThird:  make(chan struct{}, 1),
	}
	startedOnce := map[anilibria.ReleaseID]*sync.Once{
		idFirst:  {},
		idSecond: {},
		idThird:  {},
	}

	duplicateHash := hashOf('d')
	updated := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	values := map[anilibria.ReleaseID][]anilibria.Torrent{
		idFirst:  {torrent(idFirst, anilibria.ReleaseTypeTV, duplicateHash, "winner-id3 [01]", updated)},
		idSecond: {torrent(idSecond, anilibria.ReleaseTypeTV, strings.ToUpper(duplicateHash), "loser-id1 [01]", updated.Add(time.Hour))},
		idThird:  {torrent(idThird, anilibria.ReleaseTypeTV, hashOf('b'), "third-id2 [01]", updated)},
	}
	client := &fakeClient{
		searchFn: func(context.Context, string) ([]anilibria.ReleaseID, error) {
			return []anilibria.ReleaseID{idFirst, idSecond, idFirst, idThird, 4, 5}, nil
		},
		torrentsFn: func(ctx context.Context, id anilibria.ReleaseID) ([]anilibria.Torrent, error) {
			ready, expected := started[id]
			if !expected {
				return nil, fmt.Errorf("unexpected release ID %d", id)
			}
			startedOnce[id].Do(func() { close(ready) })
			select {
			case <-release[id]:
				finished[id] <- struct{}{}
				return values[id], nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}
	service := newTestService(t, client, func(config *Config) {
		config.MaxReleasesPerSearch = 3
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := make(chan executeResult, 1)
	go func() {
		response, err := service.Execute(ctx, Request{Query: "Example", Categories: allCategories(t), Limit: 50})
		result <- executeResult{response: response, err: err}
	}()

	for _, id := range []anilibria.ReleaseID{idFirst, idSecond, idThird} {
		waitClosed(t, started[id], "release branch did not start")
	}
	// Complete in the reverse of the retained release-ID order. Completion
	// order must not decide which duplicate torrent survives.
	for _, id := range []anilibria.ReleaseID{idThird, idSecond, idFirst} {
		close(release[id])
		waitSignal(t, finished[id], "release branch did not finish")
	}

	got := <-result
	if got.err != nil {
		t.Fatalf("Execute() error = %v", got.err)
	}
	if got.response.Stats.ReleaseCount != 3 || got.response.Stats.FailedBranches != 0 {
		t.Errorf("stats = %+v", got.response.Stats)
	}
	if got.response.Feed.Total != 2 || len(got.response.Feed.Items) != 2 {
		t.Fatalf("feed = %+v", got.response.Feed)
	}

	var duplicate *torznab.Item
	for index := range got.response.Feed.Items {
		if got.response.Feed.Items[index].InfoHash == duplicateHash {
			duplicate = &got.response.Feed.Items[index]
		}
	}
	if duplicate == nil || !strings.Contains(duplicate.Title, "winner-id3") {
		t.Fatalf("input-order duplicate winner was not retained: %+v", got.response.Feed.Items)
	}

	_, torrentCalls, _ := client.callSnapshot()
	slices.Sort(torrentCalls)
	if !slices.Equal(torrentCalls, []anilibria.ReleaseID{idSecond, idThird, idFirst}) {
		t.Fatalf("torrent calls = %v, want the first three distinct IDs", torrentCalls)
	}
}

func TestExecuteKeepsSuccessfulBranchesAndFailsWhenAllBranchesFail(t *testing.T) {
	t.Run("partial failure", func(t *testing.T) {
		upstreamFailure := errors.New("release unavailable")
		client := &fakeClient{
			searchFn: func(context.Context, string) ([]anilibria.ReleaseID, error) {
				return []anilibria.ReleaseID{1, 2}, nil
			},
			torrentsFn: func(_ context.Context, id anilibria.ReleaseID) ([]anilibria.Torrent, error) {
				if id == 1 {
					return nil, upstreamFailure
				}
				return []anilibria.Torrent{torrent(2, anilibria.ReleaseTypeTV, hashOf('a'), "success [01]", testTime(2))}, nil
			},
		}
		service := newTestService(t, client, nil)

		response, err := service.Execute(context.Background(), Request{Query: "Example", Categories: allCategories(t), Limit: 50})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if response.Feed.Total != 1 || len(response.Feed.Items) != 1 || response.Feed.Items[0].InfoHash != hashOf('a') {
			t.Errorf("feed = %+v", response.Feed)
		}
		if response.Stats.ReleaseCount != 2 || response.Stats.FailedBranches != 1 {
			t.Errorf("stats = %+v", response.Stats)
		}
	})

	t.Run("all branches fail", func(t *testing.T) {
		client := &fakeClient{
			searchFn: func(context.Context, string) ([]anilibria.ReleaseID, error) {
				return []anilibria.ReleaseID{1, 2}, nil
			},
			torrentsFn: func(context.Context, anilibria.ReleaseID) ([]anilibria.Torrent, error) {
				return nil, errors.New("release unavailable")
			},
		}
		service := newTestService(t, client, nil)

		_, err := service.Execute(context.Background(), Request{Query: "Example", Categories: allCategories(t), Limit: 50})
		assertServiceError(t, err, ErrorUpstream, "")
		_, torrentCalls, _ := client.callSnapshot()
		if len(torrentCalls) != 2 {
			t.Fatalf("torrent calls = %v", torrentCalls)
		}
	})
}

func TestExecuteDeduplicatesBeforeSortingThenPages(t *testing.T) {
	oldDuplicate := torrent(1, anilibria.ReleaseTypeTV, hashOf('c'), "first duplicate [01]", testTime(1))
	newDuplicate := torrent(1, anilibria.ReleaseTypeTV, strings.ToUpper(hashOf('c')), "later duplicate [01]", testTime(5))
	client := &fakeClient{
		latestFn: func(context.Context) ([]anilibria.Torrent, error) {
			return []anilibria.Torrent{
				oldDuplicate,
				torrent(1, anilibria.ReleaseTypeTV, hashOf('a'), "newest [01]", testTime(4)),
				torrent(1, anilibria.ReleaseTypeTV, hashOf('d'), "tie d [01]", testTime(3)),
				newDuplicate,
				torrent(1, anilibria.ReleaseTypeTV, hashOf('b'), "tie b [01]", testTime(3)),
			}, nil
		},
	}
	service := newTestService(t, client, nil)

	response, err := service.Execute(context.Background(), Request{
		Query:      "",
		Categories: allCategories(t),
		Offset:     1,
		Limit:      2,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if response.Feed.Total != 4 || response.Feed.Offset != 1 || response.Stats.ResultCount != 2 {
		t.Errorf("paging metadata: feed=%+v stats=%+v", response.Feed, response.Stats)
	}
	wantHashes := []string{hashOf('b'), hashOf('d')}
	gotHashes := itemHashes(response.Feed.Items)
	if !slices.Equal(gotHashes, wantHashes) {
		t.Errorf("paged hashes = %v, want %v", gotHashes, wantHashes)
	}
	// The later duplicate would sort first if deduplication happened after
	// sorting. Its absence proves the normative first-input winner was used.
	for _, item := range response.Feed.Items {
		if strings.Contains(item.Title, "later duplicate") {
			t.Fatalf("later duplicate unexpectedly survived: %+v", item)
		}
	}
}

func TestExecuteAppliesNormalizedSeasonEpisodeAndCategoryFilters(t *testing.T) {
	client := &fakeClient{
		searchFn: func(context.Context, string) ([]anilibria.ReleaseID, error) {
			return []anilibria.ReleaseID{1, 2, 3, 4}, nil
		},
		torrentsFn: func(_ context.Context, id anilibria.ReleaseID) ([]anilibria.Torrent, error) {
			values := map[anilibria.ReleaseID]anilibria.Torrent{
				1: torrent(1, anilibria.ReleaseTypeTV, hashOf('a'), "Example Season 2 [03-05]", testTime(1)),
				2: torrent(2, anilibria.ReleaseTypeTV, hashOf('b'), "Example Season 1 [04]", testTime(1)),
				3: torrent(3, anilibria.ReleaseTypeDorama, hashOf('c'), "Example Season 2 [04]", testTime(1)),
				4: torrent(4, anilibria.ReleaseTypeTV, hashOf('d'), "Example Season 2 [06]", testTime(1)),
			}
			return []anilibria.Torrent{values[id]}, nil
		},
	}
	service := newTestService(t, client, nil)
	animeOnly := categoryFilter(t, "5070")

	response, err := service.Execute(context.Background(), Request{
		Query:      "Example S02E04",
		TVSearch:   true,
		Categories: animeOnly,
		Limit:      50,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := itemHashes(response.Feed.Items); !slices.Equal(got, []string{hashOf('a')}) {
		t.Errorf("anime-filtered hashes = %v", got)
	}
	searchCalls, _, _ := client.callSnapshot()
	if !slices.Equal(searchCalls, []string{"Example"}) {
		t.Errorf("normalized upstream queries = %v", searchCalls)
	}

	// Torznab's TV parent category includes its Anime child, so the same
	// season/episode filter now retains both anime and dorama results.
	response, err = service.Execute(context.Background(), Request{
		Query:      "Example S02E04",
		TVSearch:   true,
		Categories: categoryFilter(t, "5000"),
		Limit:      50,
	})
	if err != nil {
		t.Fatalf("parent-category Execute() error = %v", err)
	}
	if got := itemHashes(response.Feed.Items); !slices.Equal(got, []string{hashOf('a'), hashOf('c')}) {
		t.Errorf("TV-parent hashes = %v", got)
	}
}

func TestExecuteCachesPositiveAndNegativeSearchBranches(t *testing.T) {
	t.Run("positive search and torrent result", func(t *testing.T) {
		client := &fakeClient{
			searchFn: func(context.Context, string) ([]anilibria.ReleaseID, error) {
				return []anilibria.ReleaseID{1}, nil
			},
			torrentsFn: func(context.Context, anilibria.ReleaseID) ([]anilibria.Torrent, error) {
				return []anilibria.Torrent{torrent(1, anilibria.ReleaseTypeTV, hashOf('a'), "cached [01]", testTime(1))}, nil
			},
		}
		service := newTestService(t, client, nil)
		request := Request{Query: "Cached", Categories: allCategories(t), Limit: 50}

		first, err := service.Execute(context.Background(), request)
		if err != nil {
			t.Fatalf("first Execute() error = %v", err)
		}
		second, err := service.Execute(context.Background(), request)
		if err != nil {
			t.Fatalf("second Execute() error = %v", err)
		}
		searchCalls, torrentCalls, _ := client.callSnapshot()
		if len(searchCalls) != 1 || len(torrentCalls) != 1 {
			t.Fatalf("upstream calls: search=%v torrents=%v", searchCalls, torrentCalls)
		}
		if first.Stats.CacheMisses != 2 || second.Stats.CacheHits != 2 || second.Stats.CacheMisses != 0 {
			t.Errorf("cache stats: first=%+v second=%+v", first.Stats, second.Stats)
		}
	})

	t.Run("negative release search", func(t *testing.T) {
		client := &fakeClient{
			searchFn: func(context.Context, string) ([]anilibria.ReleaseID, error) {
				return []anilibria.ReleaseID{}, nil
			},
		}
		service := newTestService(t, client, nil)
		request := Request{Query: "Missing", Categories: allCategories(t), Limit: 50}

		first, err := service.Execute(context.Background(), request)
		if err != nil {
			t.Fatalf("first Execute() error = %v", err)
		}
		second, err := service.Execute(context.Background(), request)
		if err != nil {
			t.Fatalf("second Execute() error = %v", err)
		}
		searchCalls, torrentCalls, _ := client.callSnapshot()
		if len(searchCalls) != 1 || len(torrentCalls) != 0 {
			t.Fatalf("upstream calls: search=%v torrents=%v", searchCalls, torrentCalls)
		}
		if first.Feed.Total != 0 || second.Feed.Total != 0 || second.Stats.CacheHits != 1 {
			t.Errorf("responses: first=%+v second=%+v", first, second)
		}
	})

	t.Run("negative per-release torrents", func(t *testing.T) {
		client := &fakeClient{
			searchFn: func(context.Context, string) ([]anilibria.ReleaseID, error) {
				return []anilibria.ReleaseID{1}, nil
			},
			torrentsFn: func(context.Context, anilibria.ReleaseID) ([]anilibria.Torrent, error) {
				return []anilibria.Torrent{}, nil
			},
		}
		service := newTestService(t, client, nil)
		request := Request{Query: "No variants", Categories: allCategories(t), Limit: 50}

		if _, err := service.Execute(context.Background(), request); err != nil {
			t.Fatalf("first Execute() error = %v", err)
		}
		second, err := service.Execute(context.Background(), request)
		if err != nil {
			t.Fatalf("second Execute() error = %v", err)
		}
		searchCalls, torrentCalls, _ := client.callSnapshot()
		if len(searchCalls) != 1 || len(torrentCalls) != 1 {
			t.Fatalf("upstream calls: search=%v torrents=%v", searchCalls, torrentCalls)
		}
		if second.Feed.Total != 0 || second.Stats.CacheHits != 2 {
			t.Errorf("cached response = %+v", second)
		}
	})
}

func TestExecuteDoesNotCacheErrors(t *testing.T) {
	client := &fakeClient{
		searchFn: func(context.Context, string) ([]anilibria.ReleaseID, error) {
			return nil, errors.New("temporary upstream error")
		},
	}
	service := newTestService(t, client, nil)
	request := Request{Query: "Retry me", Categories: allCategories(t), Limit: 50}

	for attempt := 0; attempt < 2; attempt++ {
		_, err := service.Execute(context.Background(), request)
		assertServiceError(t, err, ErrorUpstream, "")
	}
	searchCalls, _, _ := client.callSnapshot()
	if len(searchCalls) != 2 {
		t.Fatalf("search calls = %v, want one per failed request", searchCalls)
	}
}

func TestExecuteCoalescesSearchAndCancelledWaiterDoesNotCancelOwner(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	client := &fakeClient{
		searchFn: func(ctx context.Context, _ string) ([]anilibria.ReleaseID, error) {
			startedOnce.Do(func() { close(started) })
			select {
			case <-release:
				return []anilibria.ReleaseID{}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}
	service := newTestService(t, client, nil)
	request := Request{Query: "Shared", Categories: allCategories(t), Limit: 50}

	ownerContext, cancelOwner := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelOwner()
	ownerResult := make(chan executeResult, 1)
	go func() {
		response, err := service.Execute(ownerContext, request)
		ownerResult <- executeResult{response: response, err: err}
	}()
	waitClosed(t, started, "owner load did not start")

	waiterContext, cancelWaiter := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancelWaiter()
	_, waiterErr := service.Execute(waiterContext, request)
	assertServiceError(t, waiterErr, ErrorUpstream, "")
	searchCalls, _, _ := client.callSnapshot()
	if len(searchCalls) != 1 {
		t.Fatalf("concurrent same-key requests made %d search calls", len(searchCalls))
	}

	close(release)
	owner := waitExecuteResult(t, ownerResult)
	if owner.err != nil {
		t.Fatalf("owner Execute() error = %v", owner.err)
	}
	if owner.response.Feed.Total != 0 {
		t.Errorf("owner feed = %+v", owner.response.Feed)
	}
}

type fakeClient struct {
	mu sync.Mutex

	searchFn   func(context.Context, string) ([]anilibria.ReleaseID, error)
	torrentsFn func(context.Context, anilibria.ReleaseID) ([]anilibria.Torrent, error)
	latestFn   func(context.Context) ([]anilibria.Torrent, error)

	searchCalls  []string
	torrentCalls []anilibria.ReleaseID
	latestCalls  int
}

func (client *fakeClient) SearchReleases(ctx context.Context, query string) ([]anilibria.ReleaseID, error) {
	client.mu.Lock()
	client.searchCalls = append(client.searchCalls, query)
	function := client.searchFn
	client.mu.Unlock()
	if function == nil {
		return nil, errors.New("unexpected SearchReleases call")
	}
	return function(ctx, query)
}

func (client *fakeClient) TorrentsByRelease(ctx context.Context, id anilibria.ReleaseID) ([]anilibria.Torrent, error) {
	client.mu.Lock()
	client.torrentCalls = append(client.torrentCalls, id)
	function := client.torrentsFn
	client.mu.Unlock()
	if function == nil {
		return nil, errors.New("unexpected TorrentsByRelease call")
	}
	return function(ctx, id)
}

func (client *fakeClient) Latest(ctx context.Context) ([]anilibria.Torrent, error) {
	client.mu.Lock()
	client.latestCalls++
	function := client.latestFn
	client.mu.Unlock()
	if function == nil {
		return nil, errors.New("unexpected Latest call")
	}
	return function(ctx)
}

func (client *fakeClient) callSnapshot() ([]string, []anilibria.ReleaseID, int) {
	client.mu.Lock()
	defer client.mu.Unlock()
	return append([]string(nil), client.searchCalls...), append([]anilibria.ReleaseID(nil), client.torrentCalls...), client.latestCalls
}

type executeResult struct {
	response Response
	err      error
}

func newTestService(t *testing.T, client Client, adjust func(*Config)) *Service {
	t.Helper()
	config := Config{
		SiteBaseURL:          "https://aniliberty.example/",
		MaxReleasesPerSearch: 10,
		CacheMaxEntries:      32,
		SearchCacheTTL:       time.Hour,
		TorrentsCacheTTL:     time.Hour,
		LatestCacheTTL:       time.Hour,
		NegativeCacheTTL:     time.Hour,
		Logger:               slog.New(slog.DiscardHandler),
	}
	if adjust != nil {
		adjust(&config)
	}
	service, err := New(client, config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return service
}

func allCategories(t *testing.T) torznab.CategoryFilter {
	t.Helper()
	filter, err := torznab.ParseCategoryFilter(nil)
	if err != nil {
		t.Fatalf("ParseCategoryFilter(nil) error = %v", err)
	}
	return filter
}

func categoryFilter(t *testing.T, raw string) torznab.CategoryFilter {
	t.Helper()
	filter, err := torznab.ParseCategoryFilter(&raw)
	if err != nil {
		t.Fatalf("ParseCategoryFilter(%q) error = %v", raw, err)
	}
	return filter
}

func torrent(id anilibria.ReleaseID, releaseType anilibria.ReleaseType, hash, label string, updatedAt time.Time) anilibria.Torrent {
	return anilibria.Torrent{
		Hash:           hash,
		Size:           1024 + int64(id),
		Label:          label,
		Magnet:         "magnet:?xt=urn:btih:" + strings.ToLower(hash),
		Seeders:        10,
		Leechers:       2,
		CompletedTimes: 5,
		UpdatedAt:      updatedAt,
		Release: anilibria.ReleaseSummary{
			ID:       id,
			Type:     releaseType,
			Year:     2026,
			MainName: fmt.Sprintf("Release %d", id),
			Alias:    fmt.Sprintf("release-%d", id),
		},
	}
}

func hashOf(digit byte) string {
	return strings.Repeat(string(digit), 40)
}

func testTime(hour int) time.Time {
	return time.Date(2026, time.July, 16, hour, 0, 0, 0, time.UTC)
}

func itemHashes(items []torznab.Item) []string {
	hashes := make([]string, len(items))
	for index := range items {
		hashes[index] = items[index].InfoHash
	}
	return hashes
}

func assertServiceError(t *testing.T, err error, kind ErrorKind, parameter string) {
	t.Helper()
	var serviceError *Error
	if !errors.As(err, &serviceError) {
		t.Fatalf("error = %v, want *service.Error", err)
	}
	if serviceError.Kind != kind || serviceError.Parameter != parameter {
		t.Fatalf("service error = %+v, want kind=%v parameter=%q", serviceError, kind, parameter)
	}
}

func waitClosed(t *testing.T, channel <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}

func waitSignal(t *testing.T, channel <-chan struct{}, message string) {
	t.Helper()
	waitClosed(t, channel, message)
}

func waitExecuteResult(t *testing.T, result <-chan executeResult) executeResult {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(time.Second):
		t.Fatal("Execute did not return")
		return executeResult{}
	}
}
