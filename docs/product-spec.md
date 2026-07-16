# Product specification

## Purpose and ownership

This document is the source of truth for the service's product goal, scope,
externally observable behavior, configuration contract, and release criteria.
Implementation structure belongs in [architecture.md](architecture.md), exact
protocol mapping belongs in [torznab-contract.md](torznab-contract.md), upstream
facts belong in [integrations/aniliberty.md](integrations/aniliberty.md), and
parsing rules belong in [title-normalization.md](title-normalization.md). Release
versioning and image publication belong in [releases.md](releases.md). Temporary
implementation plans and work logs do not belong here.

**Status:** implementation baseline, pre-release

**Last updated:** 2026-07-16

The terms **MUST**, **SHOULD**, and **MAY** are normative.

## Problem

Prowlarr can consume a Generic Torznab indexer, but AniLiberty does not expose a
Torznab search API. AniLiberty release search returns releases without torrents;
torrent variants require a follow-up request for every release. Cardigann cannot
express this dynamic fan-out, and the latest-torrents endpoint cannot search.

The product is a single-purpose adapter between those two contracts.

## Users

The primary user runs Prowlarr and Sonarr and wants AniLiberty torrent variants
to behave like results from a conventional Torznab indexer. The service is
intended for a small self-hosted deployment, including Docker Compose on a NAS.

## Goals

The first release MUST:

1. expose a Torznab API that Prowlarr can configure as Generic Torznab;
2. translate free-text search into AniLiberty release search plus bounded
   per-release torrent lookup;
3. expose the latest 50 upstream torrents for RSS-style requests;
4. return a separate, deterministic RSS item for every torrent variant;
5. preserve magnet links and accurately map size, date, infohash, seeders,
   leechers, peers, grabs, and category;
6. produce deterministic Sonarr-friendly titles without dropping results only
   because a season or episode token could not be parsed;
7. protect the upstream with caching, request coalescing, rate limiting, bounded
   concurrency, timeouts, response limits, and bounded retries;
8. remain stateless, observable, and safe to stop;
9. ship as one Go binary in a non-root container; and
10. be deployable from public documentation without reading the source.

## Non-goals for the first release

- a web interface or user accounts;
- a database or persistent application state;
- automatic failover between AniLiberty API domains;
- deployment to any particular production host;
- proxying `.torrent` files;
- IMDb, TMDB, TVDB, TVMaze, or other metadata-ID search;
- movie, music, book, or general-purpose indexer APIs;
- reproducing every optional Newznab or Torznab feature.

## HTTP surface

The service MUST expose:

| Request | Behavior |
| --- | --- |
| `GET /api?t=caps&apikey=...` | Torznab capabilities |
| `GET /api?t=search&q=...&apikey=...` | free-text release search and fan-out |
| `GET /api?t=search&apikey=...` | latest upstream torrents, at most 50 |
| `GET /api?t=tvsearch&q=...&season=...&ep=...&apikey=...` | TV search and season/episode filtering |
| `GET /healthz` | local process health without authentication or upstream I/O |

All `/api` operations MUST require the configured API key, including `caps`.
Protocol-level failures MUST use Torznab/Newznab XML errors as specified in
[torznab-contract.md](torznab-contract.md).

## Search behavior

For non-empty `search` and all `tvsearch` requests, the service MUST:

1. validate authentication and parameters;
2. normalize technical season/episode tokens out of the upstream query without
   removing meaningful title text, producing effective filters from those
   tokens when explicit parameters are absent;
3. call AniLiberty release search and retain the returned release IDs;
4. retain at most `MAX_RELEASES_PER_SEARCH` distinct release IDs in upstream
   order;
5. fetch torrents for those releases with shared rate limiting and bounded
   concurrency;
6. retain successful release results when another release lookup fails;
7. normalize infohashes to lowercase and deduplicate by infohash;
8. derive title metadata and category;
9. apply category, season, and episode filters;
10. sort deterministically, then apply `offset` and `limit`; and
11. render Torznab XML.

An error for one release MUST NOT fail the whole search. If every required
upstream operation fails and no result can be returned, the service MUST return
an XML error rather than an empty successful feed. A genuinely empty successful
upstream response MUST produce an empty successful feed.

`tvsearch` requires a non-empty normalized `q`. A blank `search` query selects
the latest-torrents flow; a blank `tvsearch` query is an incorrect request.

## Latest/RSS behavior

A `search` request with missing, empty, or whitespace-only `q` MUST request
`/anime/torrents?limit=50` with an explicit minimal `include` list. The first
release does not page beyond this 50-item upstream window. Filtering, sorting,
deduplication, `offset`, and `limit` apply within that window.

The Torznab response's `total` therefore describes the filtered window held by
the service, not AniLiberty's global torrent count.

## Result requirements

Every valid torrent MUST become one RSS `<item>`. Its GUID MUST be derived only
from the normalized infohash and remain stable across queries and restarts.

Results MUST use magnet links as the grab mechanism and MUST include:

- title;
- GUID;
- magnet link and enclosure;
- AniLiberty release details link;
- publication date derived from `updated_at`;
- size in bytes;
- standard TV category;
- seeders, leechers, peers, grabs, and lowercase infohash;
- `downloadvolumefactor=0` and `uploadvolumefactor=1`.

The service MUST use an XML encoder rather than string interpolation so ampersands
and other characters in magnet URIs are escaped correctly. Upstream strings must
also be validated for XML 1.0 characters per item; one invalid item MUST NOT
prevent valid siblings from being serialized.

## Categories

The mapping is fixed for the first release:

| AniLiberty release type | Torznab category |
| --- | --- |
| `DORAMA` | `5000` — TV |
| `TV`, `MOVIE`, `OVA`, `ONA`, `SPECIAL`, `WEB`, `OAD` | `5070` — TV/Anime |

Unknown upstream release types MUST be logged without the raw query and omitted;
they MUST NOT silently default to another category.

## Configuration

Configuration is read from environment variables at process start. Invalid
configuration MUST prevent startup with a clear error that does not disclose
secrets.

| Variable | Required | Default | Meaning |
| --- | --- | --- | --- |
| `LISTEN_ADDR` | no | `:8080` | HTTP listen address |
| `API_KEY` | yes | none | shared key required by `/api` |
| `ANILIBRIA_API_BASE_URL` | no | `https://anilibria.top/api/v1/` | selected upstream API root |
| `ANILIBRIA_SITE_BASE_URL` | no | `https://aniliberty.top/` | public release/details root |
| `REQUEST_TIMEOUT` | no | `90s` | overall deadline for one `/api` request |
| `HTTP_TIMEOUT` | no | `15s` | timeout for one upstream attempt |
| `REQUEST_INTERVAL` | no | `2.1s` | minimum interval between upstream attempt starts |
| `MAX_CONCURRENCY` | no | `4` | maximum simultaneous upstream attempts process-wide |
| `MAX_RELEASES_PER_SEARCH` | no | `10` | maximum fan-out width |
| `MAX_RESPONSE_BYTES` | no | `8MiB` | maximum decompressed upstream response body |
| `SEARCH_CACHE_TTL` | no | `10m` | release-search cache lifetime |
| `TORRENTS_CACHE_TTL` | no | `15m` | per-release torrent cache lifetime |
| `LATEST_CACHE_TTL` | no | `5m` | latest-torrent cache lifetime |
| `NEGATIVE_CACHE_TTL` | no | `1m` | lifetime for successful empty results |
| `CACHE_MAX_ENTRIES` | no | `1024` | maximum entries in each logical cache |
| `LOG_LEVEL` | no | `info` | structured log threshold |

Durations use Go duration syntax. Byte sizes accept an integer byte count or the
documented binary suffixes `KiB`, `MiB`, and `GiB`.

Validation is explicit; zero never means "disable":

- `API_KEY` is compared exactly as supplied and must contain 1..1024 bytes.
- `LISTEN_ADDR` must be non-empty and must successfully bind during startup.
- Both base URLs must be absolute `https` URLs with a host and no user info,
  query, or fragment. Their path prefix is allowed and normalized to one trailing
  slash.
- `HTTP_TIMEOUT` must be between `100ms` and `2m`.
- `REQUEST_TIMEOUT` must be between `1s` and `10m` and not shorter than
  `HTTP_TIMEOUT`.
- `REQUEST_INTERVAL` must be between `10ms` and `1m`.
- Every cache TTL must be between `1s` and `24h`.
- `MAX_CONCURRENCY` must be in `1..64` and
  `MAX_RELEASES_PER_SEARCH` in `1..50`.
- `MAX_RESPONSE_BYTES` must be between `1KiB` and `64MiB`.
- `CACHE_MAX_ENTRIES` must be in `1..1000000`.
- `LOG_LEVEL` is one of `debug`, `info`, `warn`, or `error`, case-insensitively.

Tests may construct clients against `httptest` HTTP URLs without weakening the
production environment parser.

Changing the API base URL is the supported way to select the official
`https://api.anilibria.app/api/v1/` mirror. The service MUST NOT switch domains
automatically.

## Upstream protection

The client MUST use one process-wide limiter for all AniLiberty requests,
including retries. It MUST retry only HTTP `429`, `500`, `502`, `503`, and `504`,
with at most three attempts total. It MUST honor both forms of `Retry-After` and
otherwise use bounded exponential backoff with jitter.

The overall request deadline includes concurrency-gate wait, limiter wait,
backoff, `Retry-After`, HTTP attempts, fan-out, and serialization. If a requested
retry delay does not fit in the remaining budget, the operation ends instead of
sleeping past the deadline.

Retries MUST fit within request cancellation. Transport failures, permanent
`4xx` responses, JSON decode failures, response-limit failures, and invalid
required data MUST NOT be retried.

## Caching

The process MUST maintain separate bounded caches for:

- normalized release-search results;
- torrents by release ID; and
- the latest-torrent window.

Successful empty responses MUST use the negative TTL. Errors MUST NOT be cached.
Concurrent misses for the same logical key MUST share one load. Successful
per-release responses remain cacheable even when another branch of the same
fan-out fails.

No cache is persisted across restarts.

## Security and privacy

- API-key comparison MUST be constant-time for both equal- and unequal-length
  values.
- Logs MUST NOT contain the API key, complete request URLs, raw query strings,
  magnet URIs, or upstream `Retry-After` values that could contain unexpected
  data.
- Structured request logs SHOULD contain operation, status, duration, result
  count, cache outcome, and a generated request ID.
- `/healthz` MUST NOT expose configuration or contact the upstream.
- The container MUST run as an unprivileged user and require no writable volume.

## Runtime behavior

The process MUST emit structured logs to standard output/error, handle SIGTERM
and SIGINT, stop accepting new requests, and allow in-flight requests a bounded
grace period before exit.

Health is local process health, not upstream availability. Once configuration
has been validated and the HTTP server is running, `/healthz` returns HTTP 200
even when AniLiberty is unavailable.

## Verification requirements

Normal tests MUST be hermetic and use saved public-API fixtures or `httptest`
servers. They MUST cover:

- configuration defaults, boundary validation, and secret-safe errors;
- season/episode parsing and query cleanup;
- category mapping;
- caps and RSS XML, including namespace, magnet escaping, and item-level
  rejection of XML-invalid text;
- API-key authentication;
- array and singleton-object release-torrent responses;
- fan-out partial failure;
- infohash deduplication and deterministic ordering;
- category, season, episode, `limit`, and `offset` filtering;
- positive/negative caching and same-key request coalescing;
- retry eligibility, backoff, `Retry-After`, rate limiting, timeout, and body
  limits; and
- graceful HTTP shutdown where practical.

The baseline quality commands are `gofmt`, `go test ./...`, and `go vet ./...`.
Race tests SHOULD run in CI when resource limits permit. A live AniLiberty smoke
test MAY exist behind an explicit environment variable and MUST NOT run as part
of ordinary tests.

## First-release acceptance criteria

The release is ready only when all of the following are true:

1. Prowlarr accepts `caps` from a Generic Torznab configuration.
2. A search for `Naruto` returns separate torrent variants.
3. A search without `q` returns the latest upstream window.
4. Returned magnet links can be handed from Prowlarr to a torrent client.
5. Size, dates, infohash, seeders, leechers, peers, and grabs match fixtures.
6. One failed release lookup does not erase successful branches.
7. Repeated and concurrent identical requests demonstrably use cache and
   request coalescing.
8. The image starts and reports healthy as a non-root user without storage.
9. Required tests and quality commands pass.
10. README deployment, upgrade, rollback, Prowlarr setup, and curl examples have
    been verified against the shipped container rather than written in advance.

## Compatibility gates still requiring implementation evidence

These are not open product decisions, but documentation MUST NOT claim them as
verified until evidence is committed:

- a real Prowlarr Generic Torznab test and search;
- a real Sonarr handoff of one magnet result;
- a captured singleton-object response from the upstream, if one can still be
  observed (the decoder requirement remains for Jackett compatibility); and
- final timeout behavior under a full ten-release rate-limited fan-out.
