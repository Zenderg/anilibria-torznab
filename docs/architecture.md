# Architecture

## Purpose and ownership

This document is the source of truth for the runtime structure,
component boundaries, data flow, concurrency model, cache ownership, and failure
semantics. Product requirements belong in [product-spec.md](product-spec.md),
wire-level behavior in [torznab-contract.md](torznab-contract.md), upstream facts
in [integrations/aniliberty.md](integrations/aniliberty.md), and parsing rules in
[title-normalization.md](title-normalization.md). Release tags and image
publication belong in [releases.md](releases.md). It is not a work log.

**Status:** implemented first-release architecture

**Last updated:** 2026-07-17

## Design constraints

- Go, with the standard library preferred.
- One statically linked application binary.
- No database or durable application state.
- Bounded memory, concurrency, fan-out, response bodies, timeouts, and retries.
- One configured upstream domain; no implicit domain failover.
- Docker Compose is the supported way to run the application during development
  and deployment.
- Application startup outside Compose is not a supported workflow. Local quality
  commands and tests remain supported.

Minimal, focused dependencies are acceptable where they replace subtle
concurrency code. `golang.org/x/sync/singleflight` and
`golang.org/x/time/rate` are candidates, not requirements; the implementation
must justify every non-standard dependency in review.

## System context

```text
Prowlarr / Sonarr
       |
       | Torznab XML over HTTP
       v
anilibria-torznab
       |
       | JSON API requests, globally rate limited
       v
AniLiberty API
```

Prowlarr is the only intended protocol client. AniLiberty is the only data
source. The adapter neither stores torrents nor participates in BitTorrent; it
only returns magnet metadata obtained from the upstream.

## Component boundaries

The initial package layout should be:

```text
cmd/server/              composition root and process lifecycle
internal/config/         environment parsing and validation
internal/httpapi/        routes, authentication, request parsing, HTTP errors
internal/service/        search/latest orchestration and result processing
internal/anilibria/      upstream HTTP client and JSON models
internal/torznab/        capabilities, categories, titles, and XML models
internal/cache/          bounded TTL cache primitives
```

Responsibilities must remain directional:

- `httpapi` knows HTTP and calls `service`; it does not call AniLiberty directly.
- `service` owns fan-out, partial-failure semantics, filtering, deduplication,
  ordering, and paging.
- `anilibria` owns upstream URLs, `include` sets, rate limiting, retries, body
  limits, JSON decoding, and upstream error classification.
- `torznab` owns protocol values and serialization, not upstream I/O.
- `cache` is generic infrastructure and does not import service or API models.
- `cmd/server` wires concrete implementations and owns signal handling.

Avoid a generic repository layer: there is no database, and wrapping the
upstream client in storage terminology obscures the actual boundary.

## Core data model

The orchestration layer should consume explicit domain values rather than raw
HTTP or XML structures:

```text
ReleaseID
  positive integer returned by release search

ReleaseSummary
  ID, Type, Year, MainName, Alias

Torrent
  Hash, Size, Label, Magnet, Seeders, Leechers, CompletedTimes, UpdatedAt,
  ReleaseSummary

Result
  Torrent plus Category, ParsedSeason, ParsedEpisodeRange, RenderedTitle
```

Required upstream fields are validated at the `anilibria` boundary. Infohash is
normalized once, before a `Torrent` enters service processing.

## Request flow: capabilities

1. `httpapi` attaches a request ID and authenticates the API key.
2. It validates `t=caps`.
3. `torznab` renders a static capabilities value.

This flow performs no upstream request and uses no cache.

## Request flow: text search

1. Authenticate and parse case-insensitive Torznab parameters.
2. Normalize the query using the title-normalization contract, yielding the
   cleaned upstream text and effective season/episode filters.
3. Load ordered release IDs through the search cache and same-key coalescer.
4. Deduplicate release IDs while preserving upstream order and truncate them to
   `MAX_RELEASES_PER_SEARCH`.
5. Submit those IDs for concurrent loading. Every cache miss must acquire the
   process-wide upstream concurrency gate capped by `MAX_CONCURRENCY`.
6. Each worker loads its release torrents through the per-release cache and
   same-key coalescer. Every actual upstream attempt passes through the one
   process-wide rate limiter.
7. Store every completed branch in a slot identified by its release's original
   input index. After all branches finish, flatten successful slots by release
   index and preserve each upstream torrent-array order. Record branch failures
   without cancelling other workers. Parent request cancellation still cancels
   this request's participation in all work.
8. If no branch succeeded and at least one failed, return a total upstream
   failure. Otherwise continue, even when some branches failed.
9. Normalize and deduplicate torrents by lowercase infohash.
10. Parse title metadata, map categories, filter, sort, page, and render XML.

The process-wide gate limits simultaneous in-flight attempts across all Prowlarr
requests, not merely within one fan-out. The rate limiter separately spaces the
start of all upstream attempts. Both are necessary because a slow request may
still overlap a later permitted request.

## Request flow: latest torrents

1. Authenticate and identify `search` with a blank normalized query.
2. Load the fixed 50-item latest window through its cache and same-key coalescer.
3. Normalize, deduplicate, map, filter, sort, page, and render as for search.

The latest cache key is constant for a configured upstream. The service does not
follow the upstream pagination links in the first release.

## Rate limiting and retries

The AniLiberty client owns one concurrency gate and one limiter shared by search,
latest, per-release loads, and retries. Each attempt acquires the concurrency
gate, waits for the limiter, performs the HTTP exchange, and releases the gate.
A rate slot is consumed per attempt, not once per high-level operation.

An upstream call has at most three attempts total. Retriable responses are `429`,
`500`, `502`, `503`, and `504`. `Retry-After` takes precedence over locally
calculated delay and supports delta seconds and an HTTP date. Otherwise the
client applies bounded exponential backoff with jitter.

Transport errors and attempt timeouts are not retried in v1. This keeps the
retry policy narrow and makes a failed network path visible instead of masking
it behind additional requests.

The concurrency wait, limiter wait, backoff wait, request construction, body
read, and decode all observe the overall request context. `REQUEST_TIMEOUT`
bounds that complete path, including every `Retry-After`. When the advertised
delay cannot complete inside the remaining deadline, the request fails without
starting another attempt.

HTTP timeout applies to each attempt. End-to-end duration can exceed that
per-attempt timeout because a bounded fan-out deliberately queues on the shared
interval, but it never exceeds `REQUEST_TIMEOUT`. This must be measured during
the Prowlarr compatibility gate.

## Cache design

There are three logical caches:

| Cache | Key | Value | TTL |
| --- | --- | --- | --- |
| release search | normalized query | ordered release IDs | `SEARCH_CACHE_TTL` |
| release torrents | decimal release ID | decoded torrents | `TORRENTS_CACHE_TTL` |
| latest | singleton key | latest 50 torrents | `LATEST_CACHE_TTL` |

Each cache is concurrency-safe, TTL-aware, and bounded to `CACHE_MAX_ENTRIES`.
Expired entries may be removed lazily, but capacity eviction must be
deterministic and documented by the implementation. LRU is preferred because it
is easy to reason about for repeated searches.

A successful empty value is cached using `NEGATIVE_CACHE_TTL`. Errors are never
cached. Cache hits return immutable data or defensive copies so downstream
sorting cannot mutate shared values.

Request coalescing sits around each cache load:

```text
read cache -> miss -> join/start one keyed load -> cache successful value -> copy
```

The coalescing key includes the operation kind to prevent a numeric release ID
from colliding with another logical cache key. A shared load owns a derived
context and a reference count of active waiters rather than inheriting the first
caller's context directly. A cancelled waiter detaches immediately. The load is
cancelled when its last waiter detaches; it continues when at least one waiter
still needs it. This prevents one caller from cancelling another while also
preventing abandoned cache fills after all callers leave.

## Result processing order

Processing order is normative because it affects pagination:

1. validate required torrent data;
2. normalize hash;
3. deduplicate by hash;
4. map category and parse title metadata;
5. apply `cat`, `season`, and `ep` filters;
6. sort by `updated_at` descending, then hash ascending;
7. compute response `total`;
8. apply `offset` and then `limit`;
9. serialize.

When duplicate hashes contain different data, retain the first occurrence from
the deterministic flattened input: release-search order first, then the torrent
array order returned for that release. Do not merge fields from multiple
responses. Concurrent completion order never participates in this choice.

## Failure semantics

Failures are classified at boundaries:

| Failure | Result |
| --- | --- |
| invalid startup configuration | process exits before serving |
| invalid/missing API key | XML code `100` |
| malformed Torznab request | XML code `200` or `201` |
| unknown/unsupported operation | XML code `202` or `203` |
| one release branch fails | log summary and return other branches |
| all release branches fail | XML code `900` |
| successful empty upstream result | empty successful RSS feed |
| latest/search root call fails | XML code `900` |
| client cancels request | stop work; HTTP layer records cancellation |

Logs for branch failures include release ID, classified error, attempt count,
and duration. They do not include raw queries, full URLs, API keys, or magnet
links.

## Authentication boundary

Authentication is middleware local to `/api`. It hashes both the presented and
configured key with SHA-256 and compares the fixed-size digests using
`crypto/subtle.ConstantTimeCompare`. This preserves constant-time comparison
even when original string lengths differ.

`/healthz` bypasses that middleware by design. No other route does.

## HTTP and process lifecycle

The HTTP server must set finite read-header, read, write, and idle timeouts. Its
write timeout must allow the configured `REQUEST_TIMEOUT` plus a small bounded
serialization margin. Response headers are committed only after request
validation and protocol result selection.

On SIGTERM or SIGINT:

1. mark the server as shutting down;
2. stop accepting new connections;
3. call `http.Server.Shutdown` with a bounded grace context;
4. cancel remaining root work if the grace period expires; and
5. flush logs through normal process exit.

The health handler returns a small fixed JSON or text body and HTTP 200 while the
server is ready to accept requests. It does not become an upstream readiness
probe.

## Observability

Use `log/slog` unless implementation evidence demonstrates a missing feature.
All logs are structured. A completed request log should include:

- generated request ID;
- Torznab operation;
- outcome class and HTTP status;
- duration;
- result count;
- release count and failed-branch count where applicable; and
- aggregate cache hit/miss information.

Debug logging may add safe state transitions but must follow the same redaction
rules. Metrics and tracing endpoints are outside first-release scope.

## Container design

The Dockerfile should use a multi-stage Go build, copy only the binary and
required CA certificates into the runtime image, declare the service port, and
run under a fixed non-zero UID/GID. The runtime filesystem and Compose service
require no persistent mount.

The image healthcheck calls `/healthz` without embedding the API key. Prefer a
runtime that already contains a suitable healthcheck tool or implement a binary
health subcommand; do not add a shell solely for the healthcheck without review.

Multi-platform builds run formatting, tests, and vet on BuildKit's native
`BUILDPLATFORM`, then cross-compile the static binary with the requested
`TARGETOS` and `TARGETARCH`. Timing-sensitive concurrency tests must not execute
under QEMU merely because the runtime target is a different architecture.

## Test seams

Time, retry sleeping, and request execution must be injectable enough for fast,
deterministic tests. The upstream client accepts an `http.Client` or transport;
service tests use a fake client interface; XML tests decode their own output
rather than relying only on string snapshots.

Concurrency tests must prove:

- same-key loads collapse to one upstream call;
- cancelling one waiter does not cancel a sibling and cancelling the last
  waiter cancels the shared load;
- distinct keys respect maximum concurrency;
- all attempt starts respect the shared interval;
- the overall deadline bounds limiter and retry waits;
- one release failure does not cancel siblings; and
- output order is independent of completion order.

Avoid production-only abstractions created solely to satisfy mocks. Small
interfaces should be declared by the consuming package.
