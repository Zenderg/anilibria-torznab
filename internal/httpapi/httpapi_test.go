package httpapi

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Zenderg/anilibria-torznab/internal/service"
	"github.com/Zenderg/anilibria-torznab/internal/torznab"
)

const testAPIKey = "private-test-api-key"

type fakeExecutor struct {
	mu       sync.Mutex
	requests []service.Request
	response service.Response
	err      error
	execute  func(context.Context, service.Request) (service.Response, error)
}

func (f *fakeExecutor) Execute(ctx context.Context, request service.Request) (service.Response, error) {
	request = cloneRequest(request)
	f.mu.Lock()
	f.requests = append(f.requests, request)
	execute := f.execute
	response := f.response
	err := f.err
	f.mu.Unlock()
	if execute != nil {
		return execute(ctx, request)
	}
	return response, err
}

func (f *fakeExecutor) Requests() []service.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]service.Request(nil), f.requests...)
}

func cloneRequest(request service.Request) service.Request {
	if request.ExplicitSeason != nil {
		value := *request.ExplicitSeason
		request.ExplicitSeason = &value
	}
	if request.ExplicitEpisode != nil {
		value := *request.ExplicitEpisode
		request.ExplicitEpisode = &value
	}
	return request
}

func TestAuthenticationIsFirstAndFailureBodyIsConstant(t *testing.T) {
	t.Parallel()
	executor := newFakeExecutor()
	api := newTestAPI(t, executor, nil, time.Second)

	requests := []string{
		"/api",
		"/api?t=caps&apikey=wrong",
		"/api?t=tvsearch&q=secret&apikey=wrong",
		"/api?t=search&limit=-1&apikey=wrong",
		"/api?t=nope&apikey=wrong",
		"/api?t=caps&apikey=wrong&apikey=wrong",
		"/api?t=caps&APIKEY=" + testAPIKey + "&apikey=" + testAPIKey,
	}
	var firstBody string
	for _, target := range requests {
		recorder := perform(api, http.MethodGet, target)
		assertProtocolError(t, recorder, torznab.ErrorIncorrectCredentials, "Incorrect user credentials")
		if firstBody == "" {
			firstBody = recorder.Body.String()
		} else if recorder.Body.String() != firstBody {
			t.Errorf("authentication failure body varies for %q", target)
		}
	}
	if calls := executor.Requests(); len(calls) != 0 {
		t.Fatalf("executor was called %d times before authentication", len(calls))
	}

	if api.authenticated(testAPIKey) != true || api.authenticated(testAPIKey+"x") != false {
		t.Fatal("digest authentication returned an unexpected result")
	}
	if strings.Contains(string(api.apiKeyDigest[:]), testAPIKey) {
		t.Fatal("API stores the plaintext key")
	}
}

func TestMalformedDuplicateAPIKeyFailsAuthenticationBeforeParsing(t *testing.T) {
	t.Parallel()
	executor := newFakeExecutor()
	api := newTestAPI(t, executor, nil, time.Second)
	queries := []string{
		"apikey=" + testAPIKey + "&apikey=%ZZ&t=caps",
		"apikey=%ZZ&apikey=" + testAPIKey + "&t=caps",
		"apikey=" + testAPIKey + "&%61pikey=%ZZ&t=caps",
	}
	for _, rawQuery := range queries {
		recorder := performRawQuery(api, rawQuery)
		assertProtocolError(t, recorder, torznab.ErrorIncorrectCredentials, "Incorrect user credentials")
	}
	if len(executor.Requests()) != 0 {
		t.Fatal("executor was called for a malformed duplicate API key")
	}
}

func TestCaseInsensitiveParametersAndSearchParsing(t *testing.T) {
	t.Parallel()
	executor := newFakeExecutor()
	api := newTestAPI(t, executor, nil, time.Second)
	target := "/api?APIKEY=" + testAPIKey + "&T=SeArCh&Q=Title&S13=SURVIVES&CAT=5000,9999&LIMIT=999&OFFSET=7&EXTENDED=1&SEASON=S02&EP=E03"
	recorder := perform(api, http.MethodGet, target)

	assertXMLSuccess(t, recorder)
	requests := executor.Requests()
	if len(requests) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(requests))
	}
	request := requests[0]
	if request.Query != "Title" || !request.QueryProvided || request.TVSearch {
		t.Errorf("query fields = %#v", request)
	}
	if request.Limit != 50 || request.Offset != 7 {
		t.Errorf("paging = limit %d offset %d", request.Limit, request.Offset)
	}
	if request.ExplicitSeason == nil || *request.ExplicitSeason != "S02" || request.ExplicitEpisode == nil || *request.ExplicitEpisode != "E03" {
		t.Errorf("explicit filters = season %v episode %v", request.ExplicitSeason, request.ExplicitEpisode)
	}
	if !request.Categories.Matches(torznab.CategoryTV) || !request.Categories.Matches(torznab.CategoryAnime) {
		t.Error("parent category did not include both TV categories")
	}
}

func TestDuplicateSingletonParametersAreRejectedCaseInsensitively(t *testing.T) {
	t.Parallel()
	tests := []struct {
		query     string
		parameter torznab.Parameter
	}{
		{"t=search&T=tvsearch", torznab.ParameterType},
		{"t=search&q=one&Q=two", torznab.ParameterQuery},
		{"t=search&cat=5000&CAT=5070", torznab.ParameterCategory},
		{"t=search&limit=1&LIMIT=2", torznab.ParameterLimit},
		{"t=search&offset=1&OFFSET=2", torznab.ParameterOffset},
		{"t=search&extended=0&EXTENDED=1", torznab.ParameterExtended},
		{"t=tvsearch&q=x&season=1&SEASON=2", torznab.ParameterSeason},
		{"t=tvsearch&q=x&ep=1&EP=2", torznab.ParameterEpisode},
	}
	for _, test := range tests {
		t.Run(string(test.parameter), func(t *testing.T) {
			t.Parallel()
			executor := newFakeExecutor()
			api := newTestAPI(t, executor, nil, time.Second)
			recorder := perform(api, http.MethodGet, "/api?apikey="+testAPIKey+"&"+test.query)
			assertProtocolError(t, recorder, torznab.ErrorIncorrectParameter, "Incorrect parameter: "+string(test.parameter))
			if len(executor.Requests()) != 0 {
				t.Fatal("executor called for duplicate singleton")
			}
		})
	}
}

func TestMalformedEscapesAreAttributedToCanonicalParameters(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		rawQuery  string
		parameter torznab.Parameter
	}{
		{"operation", "apikey=" + testAPIKey + "&t=%ZZ", torznab.ParameterType},
		{"query", "apikey=" + testAPIKey + "&t=search&q=%ZZ", torznab.ParameterQuery},
		{"category", "apikey=" + testAPIKey + "&t=search&cat=%ZZ", torznab.ParameterCategory},
		{"limit", "apikey=" + testAPIKey + "&t=search&limit=%ZZ", torznab.ParameterLimit},
		{"offset", "apikey=" + testAPIKey + "&t=search&offset=%ZZ", torznab.ParameterOffset},
		{"extended", "apikey=" + testAPIKey + "&t=search&extended=%ZZ", torznab.ParameterExtended},
		{"season", "apikey=" + testAPIKey + "&t=tvsearch&q=Title&season=%ZZ", torznab.ParameterSeason},
		{"episode", "apikey=" + testAPIKey + "&t=tvsearch&q=Title&ep=%ZZ", torznab.ParameterEpisode},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			executor := newFakeExecutor()
			api := newTestAPI(t, executor, nil, time.Second)
			recorder := performRawQuery(api, test.rawQuery)
			assertProtocolError(t, recorder, torznab.ErrorIncorrectParameter, "Incorrect parameter: "+string(test.parameter))
			if len(executor.Requests()) != 0 {
				t.Fatal("executor called for malformed query escape")
			}
		})
	}
}

func TestInvalidUTF8ValuesAreRejectedAtHTTPBoundary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		rawQuery  string
		parameter torznab.Parameter
	}{
		{"operation", "apikey=" + testAPIKey + "&t=%FF", torznab.ParameterType},
		{"query", "apikey=" + testAPIKey + "&t=search&q=%FF", torznab.ParameterQuery},
		{"category", "apikey=" + testAPIKey + "&t=search&cat=%FF", torznab.ParameterCategory},
		{"limit", "apikey=" + testAPIKey + "&t=search&limit=%FF", torznab.ParameterLimit},
		{"offset", "apikey=" + testAPIKey + "&t=search&offset=%FF", torznab.ParameterOffset},
		{"extended", "apikey=" + testAPIKey + "&t=search&extended=%FF", torznab.ParameterExtended},
		{"season", "apikey=" + testAPIKey + "&t=tvsearch&q=Title&season=%FF", torznab.ParameterSeason},
		{"episode", "apikey=" + testAPIKey + "&t=tvsearch&q=Title&ep=%FF", torznab.ParameterEpisode},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			executor := newFakeExecutor()
			api := newTestAPI(t, executor, nil, time.Second)
			recorder := performRawQuery(api, test.rawQuery)
			assertProtocolError(t, recorder, torznab.ErrorIncorrectParameter, "Incorrect parameter: "+string(test.parameter))
			if len(executor.Requests()) != 0 {
				t.Fatal("executor called for invalid UTF-8")
			}
		})
	}
}

func TestCapsIsAuthenticatedFixedAndDoesNotExecuteSearch(t *testing.T) {
	t.Parallel()
	executor := newFakeExecutor()
	api := newTestAPI(t, executor, nil, time.Second)
	recorder := perform(api, http.MethodGet, "/api?apikey="+testAPIKey+"&t=CAPS&q=ignored&cat=9999&limit=0")

	assertXMLSuccess(t, recorder)
	for _, expected := range []string{
		`<caps>`,
		`<server version="1.3" title="AniLiberty Torznab">`,
		`<limits max="50" default="50">`,
		`supportedParams="q,season,ep"`,
		`<category id="5000" name="TV">`,
		`<subcat id="5070" name="Anime">`,
	} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Errorf("caps missing %q:\n%s", expected, recorder.Body.String())
		}
	}
	if len(executor.Requests()) != 0 {
		t.Fatal("caps called executor")
	}
}

func TestLargeNumericParametersFollowPagingAndCategoryContracts(t *testing.T) {
	t.Parallel()
	executor := newFakeExecutor()
	executor.execute = func(_ context.Context, request service.Request) (service.Response, error) {
		return service.Response{Feed: torznab.Feed{
			SiteBaseURL: "https://aniliberty.top/",
			Offset:      request.Offset,
		}}, nil
	}
	api := newTestAPI(t, executor, nil, time.Second)
	recorder := perform(api, http.MethodGet,
		"/api?apikey="+testAPIKey+"&t=search&limit=2147483648&offset=2147483648&cat=2147483648")
	assertXMLSuccess(t, recorder)
	if !strings.Contains(recorder.Body.String(), `<newznab:response offset="2147483648"`) {
		t.Fatalf("response did not retain the requested offset: %s", recorder.Body.String())
	}

	requests := executor.Requests()
	if len(requests) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(requests))
	}
	request := requests[0]
	if request.Limit != 50 || request.Offset != uint64(1)<<31 {
		t.Errorf("paging = limit %d offset %d", request.Limit, request.Offset)
	}
	if request.Categories.Matches(torznab.CategoryTV) || request.Categories.Matches(torznab.CategoryAnime) {
		t.Error("large unknown category was not ignored")
	}

	executor = newFakeExecutor()
	executor.execute = func(_ context.Context, request service.Request) (service.Response, error) {
		return service.Response{Feed: torznab.Feed{
			SiteBaseURL: "https://aniliberty.top/",
			Offset:      request.Offset,
		}}, nil
	}
	api = newTestAPI(t, executor, nil, time.Second)
	recorder = perform(api, http.MethodGet,
		"/api?apikey="+testAPIKey+"&t=search&limit=999999999999999999999999999999&offset=18446744073709551615")
	assertXMLSuccess(t, recorder)
	if !strings.Contains(recorder.Body.String(), `<newznab:response offset="18446744073709551615"`) {
		t.Fatalf("response did not retain the maximum offset: %s", recorder.Body.String())
	}
	request = executor.Requests()[0]
	if request.Limit != 50 || request.Offset != ^uint64(0) {
		t.Errorf("maximum paging = limit %d offset %d", request.Limit, request.Offset)
	}
}

func TestSearchLatestAndTVSearchRequestShapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		target string
		check  func(*testing.T, service.Request)
	}{
		{
			name:   "latest",
			target: "/api?apikey=" + testAPIKey + "&t=search",
			check: func(t *testing.T, request service.Request) {
				if request.Query != "" || request.QueryProvided || request.TVSearch || request.Limit != 50 || request.Offset != 0 {
					t.Errorf("latest request = %#v", request)
				}
			},
		},
		{
			name:   "blank query is present",
			target: "/api?apikey=" + testAPIKey + "&t=search&q=",
			check: func(t *testing.T, request service.Request) {
				if request.Query != "" || !request.QueryProvided || request.TVSearch {
					t.Errorf("blank-query request = %#v", request)
				}
			},
		},
		{
			name:   "tv search",
			target: "/api?apikey=" + testAPIKey + "&t=tvsearch&q=Title+S02E03&season=2&ep=3&limit=0&offset=42&extended=0",
			check: func(t *testing.T, request service.Request) {
				if request.Query != "Title S02E03" || !request.QueryProvided || !request.TVSearch || request.Limit != 0 || request.Offset != 42 {
					t.Errorf("tvsearch request = %#v", request)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			executor := newFakeExecutor()
			api := newTestAPI(t, executor, nil, time.Second)
			recorder := perform(api, http.MethodGet, test.target)
			assertXMLSuccess(t, recorder)
			requests := executor.Requests()
			if len(requests) != 1 {
				t.Fatalf("executor calls = %d, want 1", len(requests))
			}
			test.check(t, requests[0])
		})
	}
}

func TestProtocolErrorMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		target      string
		executorErr error
		code        torznab.ErrorCode
		description string
	}{
		{"missing operation", "/api?apikey=" + testAPIKey, nil, torznab.ErrorMissingParameter, "Missing parameter: t"},
		{"missing tv query", "/api?apikey=" + testAPIKey + "&t=tvsearch", nil, torznab.ErrorMissingParameter, "Missing parameter: q"},
		{"blank operation", "/api?apikey=" + testAPIKey + "&t=", nil, torznab.ErrorIncorrectParameter, "Incorrect parameter: t"},
		{"malformed category", "/api?apikey=" + testAPIKey + "&t=search&cat=5000,", nil, torznab.ErrorIncorrectParameter, "Incorrect parameter: cat"},
		{"negative limit", "/api?apikey=" + testAPIKey + "&t=search&limit=-1", nil, torznab.ErrorIncorrectParameter, "Incorrect parameter: limit"},
		{"non-numeric offset", "/api?apikey=" + testAPIKey + "&t=search&offset=x", nil, torznab.ErrorIncorrectParameter, "Incorrect parameter: offset"},
		{"invalid extended", "/api?apikey=" + testAPIKey + "&t=search&extended=true", nil, torznab.ErrorIncorrectParameter, "Incorrect parameter: extended"},
		{"service parameter", "/api?apikey=" + testAPIKey + "&t=search&q=S02E03", &service.Error{Kind: service.ErrorIncorrectParameter, Parameter: "q"}, torznab.ErrorIncorrectParameter, "Incorrect parameter: q"},
		{"unknown function", "/api?apikey=" + testAPIKey + "&t=nope", nil, torznab.ErrorNoSuchFunction, "No such function"},
		{"known unavailable", "/api?apikey=" + testAPIKey + "&t=movie", nil, torznab.ErrorFunctionUnavailable, "Function not available"},
		{"upstream classification", "/api?apikey=" + testAPIKey + "&t=search&q=Title", &service.Error{Kind: service.ErrorUpstream}, torznab.ErrorUpstreamFailed, "Upstream request failed"},
		{"unknown execution error", "/api?apikey=" + testAPIKey + "&t=search&q=Title", errors.New("private internal detail"), torznab.ErrorUpstreamFailed, "Upstream request failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			executor := newFakeExecutor()
			executor.err = test.executorErr
			api := newTestAPI(t, executor, nil, time.Second)
			recorder := perform(api, http.MethodGet, test.target)
			assertProtocolError(t, recorder, test.code, test.description)
			if strings.Contains(recorder.Body.String(), "private internal detail") {
				t.Fatal("internal execution error leaked into protocol response")
			}
		})
	}
}

func TestUnsupportedQueryNumericContinuationsReturnHTTPError(t *testing.T) {
	t.Parallel()
	queries := []string{
		"Title+S02E03.5",
		"Title+S02E03-04",
		"Title+Episode+1-2",
		"Title+Season+2.5",
		"Title+S02.5E03",
	}
	for _, query := range queries {
		executor := newFakeExecutor()
		executor.execute = func(_ context.Context, request service.Request) (service.Response, error) {
			if _, err := torznab.NormalizeQuery(request.Query, request.ExplicitSeason, request.ExplicitEpisode); err != nil {
				return service.Response{}, &service.Error{Kind: service.ErrorIncorrectParameter, Parameter: "q"}
			}
			return executor.response, nil
		}
		api := newTestAPI(t, executor, nil, time.Second)
		recorder := perform(api, http.MethodGet, "/api?apikey="+testAPIKey+"&t=search&q="+query)
		assertProtocolError(t, recorder, torznab.ErrorIncorrectParameter, "Incorrect parameter: q")
	}
}

func TestRequestDeadlineMapsToUpstreamError(t *testing.T) {
	t.Parallel()
	executor := newFakeExecutor()
	executor.execute = func(ctx context.Context, _ service.Request) (service.Response, error) {
		<-ctx.Done()
		return service.Response{}, ctx.Err()
	}
	api := newTestAPI(t, executor, nil, 10*time.Millisecond)
	recorder := perform(api, http.MethodGet, "/api?apikey="+testAPIKey+"&t=search&q=Title")
	assertProtocolError(t, recorder, torznab.ErrorUpstreamFailed, "Upstream request failed")
}

func TestRequestDeadlineOverridesSuccessfulExecutorResponse(t *testing.T) {
	t.Parallel()
	executor := newFakeExecutor()
	executor.execute = func(ctx context.Context, _ service.Request) (service.Response, error) {
		<-ctx.Done()
		return newFakeExecutor().response, nil
	}
	api := newTestAPI(t, executor, nil, 10*time.Millisecond)
	recorder := perform(api, http.MethodGet, "/api?apikey="+testAPIKey+"&t=search&q=Title")
	assertProtocolError(t, recorder, torznab.ErrorUpstreamFailed, "Upstream request failed")
}

func TestRoutingFailuresAndHealth(t *testing.T) {
	t.Parallel()
	api := newTestAPI(t, newFakeExecutor(), nil, time.Second)

	unknown := perform(api, http.MethodGet, "/unknown")
	if unknown.Code != http.StatusNotFound || strings.Contains(unknown.Body.String(), "<error") {
		t.Errorf("unknown route response = %d %q", unknown.Code, unknown.Body.String())
	}
	for _, path := range []string{"/api", "/healthz"} {
		recorder := perform(api, http.MethodPost, path)
		if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != http.MethodGet {
			t.Errorf("POST %s response = %d Allow=%q", path, recorder.Code, recorder.Header().Get("Allow"))
		}
		if strings.Contains(recorder.Body.String(), "<error") {
			t.Errorf("POST %s returned a protocol error", path)
		}
	}
	health := perform(api, http.MethodGet, "/healthz")
	if health.Code != http.StatusOK || health.Body.String() != "ok\n" || health.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("health response = %d %q %q", health.Code, health.Header().Get("Content-Type"), health.Body.String())
	}
}

func TestStructuredLogsDoNotLeakQueryKeyOrInternalErrors(t *testing.T) {
	t.Parallel()
	secretKey := testAPIKey + "-unique-secret"
	secretQuery := "unique private query text"
	privateError := "unique upstream URL and raw body"
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	executor := newFakeExecutor()
	executor.err = errors.New(privateError)
	api, err := New(Config{APIKey: secretKey, RequestTimeout: time.Second, Executor: executor, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}

	recorder := perform(api, http.MethodGet, "/api?apikey="+secretKey+"&t=search&q="+strings.ReplaceAll(secretQuery, " ", "+"))
	assertProtocolError(t, recorder, torznab.ErrorUpstreamFailed, "Upstream request failed")
	logged := logs.String()
	for _, sensitive := range []string{secretKey, secretQuery, "unique private", privateError, "raw body"} {
		if strings.Contains(logged, sensitive) {
			t.Errorf("logs leaked %q: %s", sensitive, logged)
		}
	}
	for _, safeField := range []string{"request_id", "operation", "outcome", "duration", "result_count", "cache_hits"} {
		if !strings.Contains(logged, safeField) {
			t.Errorf("logs missing safe field %q: %s", safeField, logged)
		}
	}
}

func TestResponseWriteFailureIsLoggedInsteadOfSuccess(t *testing.T) {
	t.Parallel()
	const privateWriteError = "private controlled write detail"
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	api := newTestAPI(t, newFakeExecutor(), logger, time.Second)
	writer := &failingResponseWriter{err: errors.New(privateWriteError)}
	request := httptest.NewRequest(http.MethodGet, "/api?apikey="+testAPIKey+"&t=search", nil)

	api.ServeHTTP(writer, request)

	logged := logs.String()
	if !strings.Contains(logged, `"outcome":"write_failed"`) {
		t.Fatalf("completed log did not report write failure: %s", logged)
	}
	if strings.Contains(logged, `"outcome":"success"`) || strings.Contains(logged, privateWriteError) {
		t.Fatalf("completed log claimed success or leaked the write error: %s", logged)
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	validExecutor := newFakeExecutor()
	for _, config := range []Config{
		{RequestTimeout: time.Second, Executor: validExecutor},
		{APIKey: string([]byte{0xff}), RequestTimeout: time.Second, Executor: validExecutor},
		{APIKey: testAPIKey, Executor: validExecutor},
		{APIKey: testAPIKey, RequestTimeout: time.Second},
	} {
		if _, err := New(config); err == nil {
			t.Errorf("New(%#v) accepted invalid configuration", config)
		}
	}
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{response: service.Response{
		Feed: torznab.Feed{SiteBaseURL: "https://aniliberty.top/"},
	}}
}

func newTestAPI(t *testing.T, executor Executor, logger *slog.Logger, timeout time.Duration) *API {
	t.Helper()
	api, err := New(Config{APIKey: testAPIKey, RequestTimeout: timeout, Executor: executor, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	return api
}

func perform(handler http.Handler, method, target string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func performRawQuery(handler http.Handler, rawQuery string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/api", nil)
	request.URL.RawQuery = rawQuery
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

type failingResponseWriter struct {
	header http.Header
	status int
	err    error
}

func (w *failingResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *failingResponseWriter) WriteHeader(status int) {
	w.status = status
}

func (w *failingResponseWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func assertXMLSuccess(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/xml; charset=utf-8" {
		t.Fatalf("Content-Type = %q", contentType)
	}
	if requestID := recorder.Header().Get("X-Request-ID"); requestID == "" {
		t.Fatal("X-Request-ID is missing")
	}
	if !strings.HasPrefix(recorder.Body.String(), xml.Header) {
		t.Fatalf("XML declaration is missing: %s", recorder.Body.String())
	}
	if err := xml.Unmarshal(recorder.Body.Bytes(), new(any)); err != nil {
		t.Fatalf("response is not valid XML: %v", err)
	}
}

func assertProtocolError(t *testing.T, recorder *httptest.ResponseRecorder, code torznab.ErrorCode, description string) {
	t.Helper()
	assertXMLSuccess(t, recorder)
	var envelope struct {
		XMLName     xml.Name `xml:"error"`
		Code        int      `xml:"code,attr"`
		Description string   `xml:"description,attr"`
	}
	if err := xml.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.XMLName.Local != "error" || envelope.Code != int(code) || envelope.Description != description {
		t.Fatalf("protocol error = %#v, want code=%d description=%q", envelope, code, description)
	}
}
