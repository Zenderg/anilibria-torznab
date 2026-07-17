// Package httpapi implements the authenticated Torznab HTTP boundary.
package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

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
	if cfg.APIKey == "" || !utf8.ValidString(cfg.APIKey) || cfg.RequestTimeout <= 0 || cfg.Executor == nil {
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
	key, validKey := rawAPIKey(r.URL.RawQuery)
	if !validKey || !a.authenticated(key) {
		a.completeProtocolError(w, torznab.ErrorIncorrectCredentials, torznab.ParameterAPIKey, requestID, "unknown", "authentication_failed", started, service.Stats{})
		return
	}
	if parseErr != nil {
		a.completeProtocolError(w, torznab.ErrorIncorrectParameter, malformedQueryParameter(r.URL.RawQuery), requestID, "unknown", "invalid_request", started, service.Stats{})
		return
	}

	for _, name := range singletonParameters {
		entries := canonical[name]
		if len(entries) > 1 || !validUTF8Values(entries) {
			a.completeProtocolError(w, torznab.ErrorIncorrectParameter, knownParameters[name], requestID, "unknown", "invalid_request", started, service.Stats{})
			return
		}
	}

	operationValues := canonical["t"]
	if len(operationValues) == 0 {
		a.completeProtocolError(w, torznab.ErrorMissingParameter, torznab.ParameterType, requestID, "unknown", "invalid_request", started, service.Stats{})
		return
	}
	operation := strings.ToLower(strings.TrimSpace(operationValues[0]))
	if operation == "" {
		a.completeProtocolError(w, torznab.ErrorIncorrectParameter, torznab.ParameterType, requestID, "unknown", "invalid_request", started, service.Stats{})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), a.requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	switch operation {
	case "caps":
		body, err := torznab.RenderCaps()
		if err != nil {
			writeErr := a.writeInternalError(w)
			a.logWriteResult(writeErr, requestID, operation, "serialization_failed", http.StatusInternalServerError, started, service.Stats{})
			return
		}
		if a.stopForContext(w, r.Context(), requestID, operation, started, service.Stats{}) {
			return
		}
		writeErr := a.writeXML(w, body)
		a.logWriteResult(writeErr, requestID, operation, "success", http.StatusOK, started, service.Stats{})
		return
	case "search", "tvsearch":
		a.serveSearch(w, r, canonical, requestID, operation, started)
		return
	default:
		if unavailableFunctions[operation] {
			a.completeProtocolError(w, torznab.ErrorFunctionUnavailable, torznab.ParameterType, requestID, "unknown", "unsupported_operation", started, service.Stats{})
		} else {
			a.completeProtocolError(w, torznab.ErrorNoSuchFunction, torznab.ParameterType, requestID, "unknown", "unsupported_operation", started, service.Stats{})
		}
	}
}

func (a *API) serveSearch(w http.ResponseWriter, r *http.Request, values map[string][]string, requestID, operation string, started time.Time) {
	query, queryProvided := single(values, "q")
	if operation == "tvsearch" && !queryProvided {
		a.completeProtocolError(w, torznab.ErrorMissingParameter, torznab.ParameterQuery, requestID, operation, "invalid_request", started, service.Stats{})
		return
	}

	categoryRaw, _ := singlePointer(values, "cat")
	categories, err := torznab.ParseCategoryFilter(categoryRaw)
	if err != nil {
		a.completeProtocolError(w, torznab.ErrorIncorrectParameter, torznab.ParameterCategory, requestID, operation, "invalid_request", started, service.Stats{})
		return
	}
	limit, ok := parseLimit(values)
	if !ok {
		a.completeProtocolError(w, torznab.ErrorIncorrectParameter, torznab.ParameterLimit, requestID, operation, "invalid_request", started, service.Stats{})
		return
	}
	offset, ok := parseOffset(values)
	if !ok {
		a.completeProtocolError(w, torznab.ErrorIncorrectParameter, torznab.ParameterOffset, requestID, operation, "invalid_request", started, service.Stats{})
		return
	}
	if extended, present := single(values, "extended"); present && extended != "0" && extended != "1" {
		a.completeProtocolError(w, torznab.ErrorIncorrectParameter, torznab.ParameterExtended, requestID, operation, "invalid_request", started, service.Stats{})
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
		if a.stopForContext(w, r.Context(), requestID, operation, started, service.Stats{}) {
			return
		}
		var serviceError *service.Error
		if errors.As(executeErr, &serviceError) && serviceError.Kind == service.ErrorIncorrectParameter {
			a.completeProtocolError(w, torznab.ErrorIncorrectParameter, torznab.Parameter(serviceError.Parameter), requestID, operation, "invalid_request", started, service.Stats{})
			return
		}
		a.completeProtocolError(w, torznab.ErrorUpstreamFailed, torznab.ParameterType, requestID, operation, "upstream_failed", started, service.Stats{})
		return
	}
	if a.stopForContext(w, r.Context(), requestID, operation, started, response.Stats) {
		return
	}

	body, renderErr := torznab.RenderRSSContext(r.Context(), response.Feed)
	if renderErr != nil {
		if a.stopForContext(w, r.Context(), requestID, operation, started, response.Stats) {
			return
		}
		writeErr := a.writeInternalError(w)
		a.logWriteResult(writeErr, requestID, operation, "serialization_failed", http.StatusInternalServerError, started, response.Stats)
		return
	}
	if a.stopForContext(w, r.Context(), requestID, operation, started, response.Stats) {
		return
	}
	writeErr := a.writeXML(w, body)
	a.logWriteResult(writeErr, requestID, operation, "success", http.StatusOK, started, response.Stats)
}

func (a *API) authenticated(value string) bool {
	presented := sha256.Sum256([]byte(value))
	return subtle.ConstantTimeCompare(presented[:], a.apiKeyDigest[:]) == 1
}

func (a *API) nextRequestID() string {
	return a.requestPrefix + "-" + strconv.FormatUint(a.requestCounter.Add(1), 10)
}

func (a *API) writeProtocolError(w http.ResponseWriter, code torznab.ErrorCode, parameter torznab.Parameter) error {
	body, err := torznab.RenderError(code, parameter)
	if err != nil {
		return a.writeInternalError(w)
	}
	return a.writeXML(w, body)
}

func (a *API) writeXML(w http.ResponseWriter, body []byte) error {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	return writeBody(w, body)
}

func (a *API) writeInternalError(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusInternalServerError)
	return writeBody(w, []byte("internal server error\n"))
}

func writeBody(w http.ResponseWriter, body []byte) error {
	written, err := w.Write(body)
	if err == nil && written != len(body) {
		return io.ErrShortWrite
	}
	return err
}

func (a *API) completeProtocolError(
	w http.ResponseWriter,
	code torznab.ErrorCode,
	parameter torznab.Parameter,
	requestID, operation, outcome string,
	started time.Time,
	stats service.Stats,
) {
	writeErr := a.writeProtocolError(w, code, parameter)
	a.logWriteResult(writeErr, requestID, operation, outcome, http.StatusOK, started, stats)
}

func (a *API) stopForContext(
	w http.ResponseWriter,
	ctx context.Context,
	requestID, operation string,
	started time.Time,
	stats service.Stats,
) bool {
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		a.logCompleted(requestID, operation, "cancelled", 499, started, stats)
		return true
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		a.completeProtocolError(w, torznab.ErrorUpstreamFailed, torznab.ParameterType, requestID, operation, "deadline_exceeded", started, stats)
		return true
	default:
		return false
	}
}

func (a *API) logWriteResult(writeErr error, requestID, operation, outcome string, status int, started time.Time, stats service.Stats) {
	if writeErr != nil {
		outcome = "write_failed"
	}
	a.logCompleted(requestID, operation, outcome, status, started, stats)
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

var singletonParameters = []string{"t", "q", "cat", "limit", "offset", "extended", "season", "ep"}

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

func rawAPIKey(rawQuery string) (string, bool) {
	var value string
	count := 0
	for _, field := range strings.Split(rawQuery, "&") {
		rawName, rawValue, _ := strings.Cut(field, "=")
		name, err := url.QueryUnescape(rawName)
		if err != nil || !strings.EqualFold(name, "apikey") {
			continue
		}
		count++
		if count > 1 {
			return "", false
		}
		value, err = url.QueryUnescape(rawValue)
		if err != nil {
			return "", false
		}
	}
	return value, count == 1
}

func malformedQueryParameter(rawQuery string) torznab.Parameter {
	for _, field := range strings.Split(rawQuery, "&") {
		rawName, rawValue, _ := strings.Cut(field, "=")
		name, err := url.QueryUnescape(rawName)
		if err != nil {
			continue
		}
		parameter, known := knownParameters[strings.ToLower(name)]
		if !known {
			continue
		}
		if strings.Contains(field, ";") {
			return parameter
		}
		if _, err := url.QueryUnescape(rawValue); err != nil {
			return parameter
		}
	}
	return torznab.ParameterType
}

func validUTF8Values(values []string) bool {
	for _, value := range values {
		if !utf8.ValidString(value) {
			return false
		}
	}
	return true
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

func parseLimit(values map[string][]string) (int, bool) {
	raw, present := single(values, "limit")
	if !present {
		return 50, true
	}
	normalized, ok := normalizedDecimal(raw)
	if !ok {
		return 0, false
	}
	if len(normalized) > 2 || len(normalized) == 2 && normalized > "50" {
		return 50, true
	}
	parsed, err := strconv.ParseUint(normalized, 10, 8)
	if err != nil {
		return 0, false
	}
	return int(parsed), true
}

func parseOffset(values map[string][]string) (uint64, bool) {
	raw, present := single(values, "offset")
	if !present {
		return 0, true
	}
	normalized, ok := normalizedDecimal(raw)
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseUint(normalized, 10, 64)
	return parsed, err == nil
}

func normalizedDecimal(raw string) (string, bool) {
	if raw == "" || strings.HasPrefix(raw, "+") || strings.HasPrefix(raw, "-") {
		return "", false
	}
	for _, character := range raw {
		if character < '0' || character > '9' {
			return "", false
		}
	}
	normalized := strings.TrimLeft(raw, "0")
	if normalized == "" {
		return "0", true
	}
	return normalized, true
}
