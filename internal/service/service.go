// Package service orchestrates searches independently of HTTP and XML rendering.
package service

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Zenderg/anilibria-torznab/internal/anilibria"
	"github.com/Zenderg/anilibria-torznab/internal/cache"
	"github.com/Zenderg/anilibria-torznab/internal/torznab"
)

// Request describes one validated Torznab search operation.
type Request struct {
	Query           string
	QueryProvided   bool
	TVSearch        bool
	ExplicitSeason  *string
	ExplicitEpisode *string
	Categories      torznab.CategoryFilter
	Limit           int
	Offset          int
}

// Stats contains safe request diagnostics for structured logging.
type Stats struct {
	ReleaseCount   int
	FailedBranches int
	CacheHits      int
	CacheMisses    int
	ResultCount    int
}

// Response is ready for Torznab XML serialization.
type Response struct {
	Feed  torznab.Feed
	Stats Stats
}

// ErrorKind is an externally safe service failure classification.
type ErrorKind uint8

const (
	ErrorIncorrectParameter ErrorKind = iota + 1
	ErrorUpstream
)

// Error maps orchestration failures to protocol-safe responses.
type Error struct {
	Kind      ErrorKind
	Parameter string
}

func (e *Error) Error() string {
	switch e.Kind {
	case ErrorIncorrectParameter:
		return "incorrect parameter"
	case ErrorUpstream:
		return "upstream request failed"
	default:
		return "service request failed"
	}
}

// Config contains service-local bounds and cache policy.
type Config struct {
	SiteBaseURL          string
	MaxReleasesPerSearch int
	CacheMaxEntries      int
	SearchCacheTTL       time.Duration
	TorrentsCacheTTL     time.Duration
	LatestCacheTTL       time.Duration
	NegativeCacheTTL     time.Duration
	Logger               *slog.Logger
}

// Client is the AniLiberty boundary consumed by the service.
type Client interface {
	SearchReleases(context.Context, string) ([]anilibria.ReleaseID, error)
	TorrentsByRelease(context.Context, anilibria.ReleaseID) ([]anilibria.Torrent, error)
	Latest(context.Context) ([]anilibria.Torrent, error)
}

// Service owns fan-out, caching, filtering, ordering, and paging.
type Service struct {
	client Client
	config Config
	logger *slog.Logger

	searchCache   *cache.LRU[string, []anilibria.ReleaseID]
	torrentsCache *cache.LRU[anilibria.ReleaseID, []anilibria.Torrent]
	latestCache   *cache.LRU[string, []anilibria.Torrent]
	searchLoads   cache.Coalescer[string, []anilibria.ReleaseID]
	torrentLoads  cache.Coalescer[anilibria.ReleaseID, []anilibria.Torrent]
	latestLoads   cache.Coalescer[string, []anilibria.Torrent]
}

// New constructs a service with three independent bounded caches.
func New(client Client, cfg Config) (*Service, error) {
	if client == nil {
		return nil, errors.New("service client is required")
	}
	if cfg.SiteBaseURL == "" || cfg.MaxReleasesPerSearch < 1 || cfg.CacheMaxEntries < 1 {
		return nil, errors.New("invalid service configuration")
	}
	if cfg.SearchCacheTTL <= 0 || cfg.TorrentsCacheTTL <= 0 || cfg.LatestCacheTTL <= 0 || cfg.NegativeCacheTTL <= 0 {
		return nil, errors.New("invalid service cache configuration")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	now := time.Now
	return &Service{
		client:        client,
		config:        cfg,
		logger:        cfg.Logger,
		searchCache:   cache.NewLRU[string, []anilibria.ReleaseID](cfg.CacheMaxEntries, now),
		torrentsCache: cache.NewLRU[anilibria.ReleaseID, []anilibria.Torrent](cfg.CacheMaxEntries, now),
		latestCache:   cache.NewLRU[string, []anilibria.Torrent](cfg.CacheMaxEntries, now),
	}, nil
}

// Execute performs a text search, TV search, or latest-window request.
func (s *Service) Execute(ctx context.Context, req Request) (Response, error) {
	normalized, err := torznab.NormalizeQuery(req.Query, req.ExplicitSeason, req.ExplicitEpisode)
	if err != nil {
		var parameterError *torznab.ParameterError
		if errors.As(err, &parameterError) {
			return Response{}, &Error{Kind: ErrorIncorrectParameter, Parameter: string(parameterError.Parameter)}
		}
		return Response{}, &Error{Kind: ErrorIncorrectParameter, Parameter: "q"}
	}

	originalBlank := strings.TrimSpace(req.Query) == ""
	if req.TVSearch && normalized.CleanQuery == "" {
		return Response{}, &Error{Kind: ErrorIncorrectParameter, Parameter: "q"}
	}
	if !req.TVSearch && !originalBlank && normalized.CleanQuery == "" {
		return Response{}, &Error{Kind: ErrorIncorrectParameter, Parameter: "q"}
	}

	var counters requestCounters
	var torrents []anilibria.Torrent
	if !req.TVSearch && originalBlank {
		torrents, err = s.loadLatest(ctx, &counters)
		if err != nil {
			return Response{}, &Error{Kind: ErrorUpstream}
		}
	} else {
		torrents, err = s.loadSearch(ctx, normalized.CleanQuery, &counters)
		if err != nil {
			return Response{}, err
		}
	}

	items := s.process(torrents, req.Categories, normalized.EffectiveSeason, normalized.EffectiveEpisode)
	items = torznab.FilterValidItems(items, s.config.SiteBaseURL)
	total := len(items)
	page := pageItems(items, req.Offset, req.Limit)
	stats := Stats{
		ReleaseCount:   int(counters.releaseCount.Load()),
		FailedBranches: int(counters.failedBranches.Load()),
		CacheHits:      int(counters.cacheHits.Load()),
		CacheMisses:    int(counters.cacheMisses.Load()),
		ResultCount:    len(page),
	}
	return Response{
		Feed: torznab.Feed{
			SiteBaseURL: s.config.SiteBaseURL,
			Offset:      req.Offset,
			Total:       total,
			Items:       page,
		},
		Stats: stats,
	}, nil
}

type requestCounters struct {
	releaseCount   atomic.Int64
	failedBranches atomic.Int64
	cacheHits      atomic.Int64
	cacheMisses    atomic.Int64
}

func (s *Service) loadSearch(ctx context.Context, query string, counters *requestCounters) ([]anilibria.Torrent, error) {
	ids, err := s.cachedSearch(ctx, query, counters)
	if err != nil {
		return nil, &Error{Kind: ErrorUpstream}
	}
	ids = distinctReleaseIDs(ids, s.config.MaxReleasesPerSearch)
	counters.releaseCount.Store(int64(len(ids)))
	if len(ids) == 0 {
		return []anilibria.Torrent{}, nil
	}

	type branch struct {
		torrents []anilibria.Torrent
		err      error
	}
	branches := make([]branch, len(ids))
	var wg sync.WaitGroup
	for index, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			branches[index].torrents, branches[index].err = s.cachedTorrents(ctx, id, counters)
		}()
	}
	wg.Wait()

	flattened := make([]anilibria.Torrent, 0)
	succeeded := 0
	for index, branch := range branches {
		if branch.err != nil {
			counters.failedBranches.Add(1)
			attributes := []any{"release_id", ids[index]}
			var upstreamError *anilibria.Error
			if errors.As(branch.err, &upstreamError) {
				attributes = append(attributes,
					"error_class", upstreamError.Class,
					"attempt_count", upstreamError.Attempts,
					"duration", upstreamError.Duration,
				)
			}
			s.logger.WarnContext(ctx, "upstream release branch failed", attributes...)
			continue
		}
		succeeded++
		flattened = append(flattened, branch.torrents...)
	}
	if succeeded == 0 {
		return nil, &Error{Kind: ErrorUpstream}
	}
	return flattened, nil
}

func (s *Service) cachedSearch(ctx context.Context, query string, counters *requestCounters) ([]anilibria.ReleaseID, error) {
	if value, ok := s.searchCache.Get(query); ok {
		counters.cacheHits.Add(1)
		return cloneIDs(value), nil
	}
	counters.cacheMisses.Add(1)
	value, err := s.searchLoads.Do(ctx, query, func(loadCtx context.Context) ([]anilibria.ReleaseID, error) {
		if cached, ok := s.searchCache.Get(query); ok {
			return cloneIDs(cached), nil
		}
		loaded, loadErr := s.client.SearchReleases(loadCtx, query)
		if loadErr != nil {
			return nil, loadErr
		}
		loaded = cloneIDs(loaded)
		s.searchCache.Set(query, loaded, cache.TTLFor(len(loaded) == 0, s.config.SearchCacheTTL, s.config.NegativeCacheTTL))
		return cloneIDs(loaded), nil
	})
	return cloneIDs(value), err
}

func (s *Service) cachedTorrents(ctx context.Context, id anilibria.ReleaseID, counters *requestCounters) ([]anilibria.Torrent, error) {
	if value, ok := s.torrentsCache.Get(id); ok {
		counters.cacheHits.Add(1)
		return cloneTorrents(value), nil
	}
	counters.cacheMisses.Add(1)
	value, err := s.torrentLoads.Do(ctx, id, func(loadCtx context.Context) ([]anilibria.Torrent, error) {
		if cached, ok := s.torrentsCache.Get(id); ok {
			return cloneTorrents(cached), nil
		}
		loaded, loadErr := s.client.TorrentsByRelease(loadCtx, id)
		if loadErr != nil {
			return nil, loadErr
		}
		loaded = cloneTorrents(loaded)
		s.torrentsCache.Set(id, loaded, cache.TTLFor(len(loaded) == 0, s.config.TorrentsCacheTTL, s.config.NegativeCacheTTL))
		return cloneTorrents(loaded), nil
	})
	return cloneTorrents(value), err
}

func (s *Service) loadLatest(ctx context.Context, counters *requestCounters) ([]anilibria.Torrent, error) {
	const key = "latest"
	if value, ok := s.latestCache.Get(key); ok {
		counters.cacheHits.Add(1)
		return cloneTorrents(value), nil
	}
	counters.cacheMisses.Add(1)
	value, err := s.latestLoads.Do(ctx, key, func(loadCtx context.Context) ([]anilibria.Torrent, error) {
		if cached, ok := s.latestCache.Get(key); ok {
			return cloneTorrents(cached), nil
		}
		loaded, loadErr := s.client.Latest(loadCtx)
		if loadErr != nil {
			return nil, loadErr
		}
		loaded = cloneTorrents(loaded)
		s.latestCache.Set(key, loaded, cache.TTLFor(len(loaded) == 0, s.config.LatestCacheTTL, s.config.NegativeCacheTTL))
		return cloneTorrents(loaded), nil
	})
	return cloneTorrents(value), err
}

func (s *Service) process(torrents []anilibria.Torrent, categories torznab.CategoryFilter, season, episode *int) []torznab.Item {
	seen := make(map[string]struct{}, len(torrents))
	items := make([]torznab.Item, 0, len(torrents))
	for _, torrent := range torrents {
		hash := strings.ToLower(torrent.Hash)
		if _, exists := seen[hash]; exists {
			continue
		}
		seen[hash] = struct{}{}

		category, ok := torznab.CategoryFor(torznab.ReleaseType(torrent.Release.Type))
		if !ok {
			s.logger.Warn("unsupported upstream release type", "release_id", torrent.Release.ID, "release_type", torrent.Release.Type)
			continue
		}
		if !categories.Matches(category) {
			continue
		}
		metadata, err := torznab.ParseTitle(torznab.ReleaseType(torrent.Release.Type), torrent.Release.MainName, torrent.Label)
		if err != nil {
			s.logger.Warn("torrent title metadata is invalid", "release_id", torrent.Release.ID)
			continue
		}
		if !metadata.Matches(season, episode) {
			continue
		}
		if torrent.Seeders > math.MaxInt64-torrent.Leechers {
			s.logger.Warn("torrent peer count overflows", "release_id", torrent.Release.ID)
			continue
		}
		var year *int
		if torrent.Release.Year > 0 {
			value := torrent.Release.Year
			year = &value
		}
		items = append(items, torznab.Item{
			Title:          metadata.Title,
			InfoHash:       hash,
			MagnetURI:      torrent.Magnet,
			ReleaseAlias:   torrent.Release.Alias,
			UpdatedAt:      torrent.UpdatedAt,
			Category:       category,
			SizeBytes:      torrent.Size,
			Seeders:        torrent.Seeders,
			Leechers:       torrent.Leechers,
			CompletedTimes: torrent.CompletedTimes,
			Year:           year,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].UpdatedAt.After(items[j].UpdatedAt)
		}
		return items[i].InfoHash < items[j].InfoHash
	})
	return items
}

func distinctReleaseIDs(input []anilibria.ReleaseID, limit int) []anilibria.ReleaseID {
	seen := make(map[anilibria.ReleaseID]struct{}, len(input))
	result := make([]anilibria.ReleaseID, 0, min(len(input), limit))
	for _, id := range input {
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
		if len(result) == limit {
			break
		}
	}
	return result
}

func pageItems(items []torznab.Item, offset, limit int) []torznab.Item {
	if offset >= len(items) || limit == 0 {
		return []torznab.Item{}
	}
	end := min(len(items), offset+limit)
	return append([]torznab.Item(nil), items[offset:end]...)
}

func cloneIDs(input []anilibria.ReleaseID) []anilibria.ReleaseID {
	return append([]anilibria.ReleaseID(nil), input...)
}

func cloneTorrents(input []anilibria.Torrent) []anilibria.Torrent {
	return append([]anilibria.Torrent(nil), input...)
}
