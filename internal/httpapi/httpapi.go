// Package httpapi implements the authenticated Torznab HTTP boundary.
package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Zenderg/anilibria-torznab/internal/service"
	"github.com/Zenderg/anilibria-torznab/internal/torznab"
)

// Executor is the service boundary consumed by the HTTP handler.
type Executor interface {
	Execute(context.Context, service.Request) (service.Response, error)
}

// Config contains HTTP-boundary dependencies and policy.
type Config struct {
	APIKey         string
	RequestTimeout time.Duration
	Executor       Executor
	Logger         *slog.Logger
}

// API handles /api and /healthz without exposing configuration details.
type API struct {
	apiKeyDigest   [sha256.Size]byte
	requestTimeout time.Duration
	executor       Executor
	logger         *slog.Logger
	requestPrefix  string
	requestCounter atomic.Uint64
}

// New validates dependencies and initializes request-ID generation.
func New(cfg Config) (*API, error) {
	if cfg.APIKey == "" || cfg.RequestTimeout <= 0 || cfg.Executor == nil {
		return nil, errors.New("invalid HTTP API configuration")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	prefixBytes := make([]byte, 8)
	if _, err := rand.Read(prefixBytes); err != nil {
		return nil, errors.New("initialize request IDs")
	}
	return &API{
		apiKeyDigest:   sha256.Sum256([]byte(cfg.APIKey)),
		requestTimeout: cfg.RequestTimeout,
		executor:       cfg.Executor,
		logger:         cfg.Logger,
		requestPrefix:  hex.EncodeToString(prefixBytes),
	}, nil
}

// ServeHTTP exposes only the documented paths.
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		a.serveHealth(w, r)
	case "/api":
		a.serveAPI(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *API) serveHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (a *API) serveAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	started := time.Now()
	requestID := a.nextRequestID()
	w.Header().Set("X-Request-ID", requestID)
	values, parseErr := url.ParseQuery(r.URL.RawQuery)
	canonical := canonicalValues(values)
	keys := canonical["apikey"]
	if len(keys) != 1 || !a.authenticated(keys[0]) {
		a.writeProtocolError(w, torznab.ErrorIncorrectCredentials, torznab.ParameterAPIKey)
		a.logCompleted(requestID, "unknown", "authentication_failed", http.StatusOK, started, service.Stats{})
		return
	}
	if parseErr != nil {
		a.writeProtocolError(w, torznab.ErrorIncorrectParameter, torznab.ParameterType)
		a.logCompleted(requestID, "unknown", "invalid_request", http.StatusOK, started, service.Stats{})
		return
	}

	for name, parameter := range knownParameters {
		if name == "apikey" {
			continue
		}
		if len(canonical[name]) > 1 {
			a.writeProtocolError(w, torznab.ErrorIncorrectParameter, parameter)
			a.logCompleted(requestID, "unknown", "invalid_request", http.StatusOK, started, service.Stats{})
			return
		}
	}

	operationValues := canonical["t"]
	if len(operationValues) == 0 {
		a.writeProtocolError(w, torznab.ErrorMissingParameter, torznab.ParameterType)
		a.logCompleted(requestID, "unknown", "invalid_request", http.StatusOK, started, service.Stats{})
		return
	}
	operation := strings.ToLower(strings.TrimSpace(operationValues[0]))
	if operation == "" {
		a.writeProtocolError(w, torznab.ErrorIncorrectParameter, torznab.ParameterType)
		a.logCompleted(requestID, "unknown", "invalid_request", http.StatusOK, started, service.Stats{})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), a.requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	switch operation {
	case "caps":
		body, err := torznab.RenderCaps()
		if err != nil {
			a.writeInternalError(w)
			a.logCompleted(requestID, operation, "serialization_failed", http.StatusInternalServerError, started, service.Stats{})
			return
		}
		a.writeXML(w, body)
		a.logCompleted(requestID, operation, "success", http.StatusOK, started, service.Stats{})
		return
	case "search", "tvsearch":
		a.serveSearch(w, r, canonical, requestID, operation, started)
		return
	default:
		if unavailableFunctions[operation] {
			a.writeProtocolError(w, torznab.ErrorFunctionUnavailable, torznab.ParameterType)
		} else {
			a.writeProtocolError(w, torznab.ErrorNoSuchFunction, torznab.ParameterType)
		}
		a.logCompleted(requestID, "unknown", "unsupported_operation", http.StatusOK, started, service.Stats{})
	}
}

func (a *API) serveSearch(w http.ResponseWriter, r *http.Request, values map[string][]string, requestID, operation string, started time.Time) {
	query, queryProvided := single(values, "q")
	if operation == "tvsearch" && !queryProvided {
		a.writeProtocolError(w, torznab.ErrorMissingParameter, torznab.ParameterQuery)
		a.logCompleted(requestID, operation, "invalid_request", http.StatusOK, started, service.Stats{})
		return
	}

	categoryRaw, _ := singlePointer(values, "cat")
	categories, err := torznab.ParseCategoryFilter(categoryRaw)
	if err != nil {
		a.writeProtocolError(w, torznab.ErrorIncorrectParameter, torznab.ParameterCategory)
		a.logCompleted(requestID, operation, "invalid_request", http.StatusOK, started, service.Stats{})
		return
	}
	limit, ok := parseBoundedCount(values, "limit", 50, 50)
	if !ok {
		a.writeProtocolError(w, torznab.ErrorIncorrectParameter, torznab.ParameterLimit)
		a.logCompleted(requestID, operation, "invalid_request", http.StatusOK, started, service.Stats{})
		return
	}
	offset, ok := parseBoundedCount(values, "offset", 0, -1)
	if !ok {
		a.writeProtocolError(w, torznab.ErrorIncorrectParameter, torznab.ParameterOffset)
		a.logCompleted(requestID, operation, "invalid_request", http.StatusOK, started, service.Stats{})
		return
	}
	if extended, present := single(values, "extended"); present && extended != "0" && extended != "1" {
		a.writeProtocolError(w, torznab.ErrorIncorrectParameter, torznab.ParameterExtended)
		a.logCompleted(requestID, operation, "invalid_request", http.StatusOK, started, service.Stats{})
		return
	}
	season, _ := singlePointer(values, "season")
	episode, _ := singlePointer(values, "ep")

	response, executeErr := a.executor.Execute(r.Context(), service.Request{
		Query:           query,
		QueryProvided:   queryProvided,
		TVSearch:        operation == "tvsearch",
		ExplicitSeason:  season,
		ExplicitEpisode: episode,
		Categories:      categories,
		Limit:           limit,
		Offset:          offset,
	})
	if executeErr != nil {
		if errors.Is(r.Context().Err(), context.Canceled) {
			a.logCompleted(requestID, operation, "cancelled", 499, started, service.Stats{})
			return
		}
		if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
			a.writeProtocolError(w, torznab.ErrorUpstreamFailed, torznab.ParameterType)
			a.logCompleted(requestID, operation, "deadline_exceeded", http.StatusOK, started, service.Stats{})
			return
		}
		var serviceError *service.Error
		if errors.As(executeErr, &serviceError) && serviceError.Kind == service.ErrorIncorrectParameter {
			a.writeProtocolError(w, torznab.ErrorIncorrectParameter, torznab.Parameter(serviceError.Parameter))
			a.logCompleted(requestID, operation, "invalid_request", http.StatusOK, started, service.Stats{})
			return
		}
		a.writeProtocolError(w, torznab.ErrorUpstreamFailed, torznab.ParameterType)
		a.logCompleted(requestID, operation, "upstream_failed", http.StatusOK, started, service.Stats{})
		return
	}

	body, renderErr := torznab.RenderRSS(response.Feed)
	if renderErr != nil {
		a.writeInternalError(w)
		a.logCompleted(requestID, operation, "serialization_failed", http.StatusInternalServerError, started, response.Stats)
		return
	}
	a.writeXML(w, body)
	a.logCompleted(requestID, operation, "success", http.StatusOK, started, response.Stats)
}

func (a *API) authenticated(value string) bool {
	presented := sha256.Sum256([]byte(value))
	return subtle.ConstantTimeCompare(presented[:], a.apiKeyDigest[:]) == 1
}

func (a *API) nextRequestID() string {
	return a.requestPrefix + "-" + strconv.FormatUint(a.requestCounter.Add(1), 10)
}

func (a *API) writeProtocolError(w http.ResponseWriter, code torznab.ErrorCode, parameter torznab.Parameter) {
	body, err := torznab.RenderError(code, parameter)
	if err != nil {
		a.writeInternalError(w)
		return
	}
	a.writeXML(w, body)
}

func (a *API) writeXML(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (a *API) writeInternalError(w http.ResponseWriter) {
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

func (a *API) logCompleted(requestID, operation, outcome string, status int, started time.Time, stats service.Stats) {
	a.logger.Info("request completed",
		"request_id", requestID,
		"operation", operation,
		"outcome", outcome,
		"status", status,
		"duration", time.Since(started),
		"result_count", stats.ResultCount,
		"release_count", stats.ReleaseCount,
		"failed_branch_count", stats.FailedBranches,
		"cache_hits", stats.CacheHits,
		"cache_misses", stats.CacheMisses,
	)
}

var knownParameters = map[string]torznab.Parameter{
	"apikey":   torznab.ParameterAPIKey,
	"t":        torznab.ParameterType,
	"q":        torznab.ParameterQuery,
	"cat":      torznab.ParameterCategory,
	"limit":    torznab.ParameterLimit,
	"offset":   torznab.ParameterOffset,
	"extended": torznab.ParameterExtended,
	"season":   torznab.ParameterSeason,
	"ep":       torznab.ParameterEpisode,
}

var unavailableFunctions = map[string]bool{
	"book": true, "details": true, "get": true, "genres": true,
	"movie": true, "music": true, "register": true,
}

func canonicalValues(values url.Values) map[string][]string {
	result := make(map[string][]string, len(values))
	for name, entries := range values {
		canonical := strings.ToLower(name)
		result[canonical] = append(result[canonical], entries...)
	}
	return result
}

func single(values map[string][]string, name string) (string, bool) {
	entries := values[name]
	if len(entries) == 0 {
		return "", false
	}
	return entries[0], true
}

func singlePointer(values map[string][]string, name string) (*string, bool) {
	value, ok := single(values, name)
	if !ok {
		return nil, false
	}
	return &value, true
}

func parseBoundedCount(values map[string][]string, name string, defaultValue, maximum int) (int, bool) {
	raw, present := single(values, name)
	if !present {
		return defaultValue, true
	}
	if raw == "" || strings.HasPrefix(raw, "+") || strings.HasPrefix(raw, "-") {
		return 0, false
	}
	parsed, err := strconv.ParseUint(raw, 10, 31)
	if err != nil {
		return 0, false
	}
	value := int(parsed)
	if maximum >= 0 && value > maximum {
		value = maximum
	}
	return value, true
}
