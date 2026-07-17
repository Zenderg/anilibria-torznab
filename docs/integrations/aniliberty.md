# AniLiberty integration contract

## Purpose and ownership

This document is the source of truth for the AniLiberty domain policy, API
endpoints, request projections, response shapes, field semantics, and verified
upstream constraints used by this service. Torznab output mapping belongs in
[../torznab-contract.md](../torznab-contract.md), orchestration in
[../architecture.md](../architecture.md), and product scope in
[../product-spec.md](../product-spec.md). Do not put transient outage notes or
general project history here.

**Validation date:** 2026-07-16

**Contract version:** AniLiberty API V1

## Authoritative references

- [interactive API documentation](https://anilibria.top/api/docs/v1)
- [OpenAPI JSON](https://anilibria.top/storage/api/docs/v1?aniliberty-api-v1-docs.json)
- [official domain/API separation announcement](https://t.me/AniLibria_Devs/74)
- [official API mirror reminder](https://t.me/AniLibria_Devs/84)
- [official service status components](https://status.anilibria.top/api-components/)
- [Jackett adapter at the reviewed commit](https://github.com/Jackett/Jackett/blob/7498d758a84253728b70de6f1d09d4c11bb3df18/src/Jackett.Common/Indexers/Definitions/Anilibria.cs), used only as compatibility evidence

OpenAPI is authoritative for declared paths and schemas. Live smoke requests are
used to validate deployment behavior. Jackett is a useful behavioral reference,
not an upstream specification.

## Naming and domains

AniLiberty is the current project/website brand; AniLibria remains in older
domains, application names, and community integrations.

The service uses separate configurable roots:

| Role | Default |
| --- | --- |
| API | `https://anilibria.top/api/v1/` |
| public site/details links | `https://aniliberty.top/` |
| operator-selectable API mirror | `https://api.anilibria.app/api/v1/` |

Official announcements identify `anilibria.top/api` as the current API and
`api.anilibria.app/api` as its mirror, while the status service identifies
`aniliberty.top` as the primary website. The current OpenAPI document also lists
`https://aniliberty.top/api/v1` as an absolute server. Smoke requests on all
three API roots returned the same release IDs and torrent infohashes on the
validation date.

Because those signals are not perfectly aligned, the base URL remains explicit
configuration. The implementation sends every request to exactly the configured
API root. It does not redirect, probe mirrors, or fail over automatically.

### Validation evidence

On the validation date, read-only smoke checks established:

- `app/search/releases?query=Naruto&include=id` returned release IDs `413`,
  `2495`, and `3996` from all three API roots;
- `anime/torrents/release/413?include=hash` returned an array with one identical
  infohash from all three roots;
- the OpenAPI document served from all three roots contained the same API title,
  version, and server list; and
- `anime/torrents?limit=51&include=hash` on the default root returned HTTP `422`
  with validation that `limit` cannot exceed 50.

These checks establish compatibility at a point in time, not an availability
guarantee. Repeat them through the opt-in smoke test when revalidating.

Base URL joining must preserve a path prefix and produce exactly one slash
between the configured root and endpoint. Trailing-slash normalization preserves
valid percent-encoded reserved path characters such as `%2F`; it must not turn
them into route separators. Only `https` upstream roots are valid unless an
explicit development/test mode is introduced later.

## Client identity

Every request MUST send a stable User-Agent in this form:

```text
anilibria-torznab/<version> (+https://github.com/Zenderg/anilibria-torznab)
```

Development builds may use `dev` as the version. The client requests JSON and
uses response compression supported by Go's default transport.

## Endpoint subset

Only the following V1 endpoints are in scope:

| Endpoint relative to API root | Use |
| --- | --- |
| `GET app/search/releases` | find release IDs |
| `GET anime/torrents/release/{releaseId}` | fetch every torrent variant for one release |
| `GET anime/torrents` | fetch the latest torrent window |

`GET anime/torrents/{hashOrId}/file` is documented upstream but is not used in
the first release because results expose magnets directly. AniLiberty's own RSS
endpoints are also not used; the JSON latest endpoint already contains the
fields required for Torznab and supports field projection.

## Release search

Request:

```http
GET app/search/releases?query=<url-encoded-query>&include=id
```

`query` is required by the upstream. It is encoded with standard URL query
encoding and is never assembled by concatenating unescaped user input.

Declared and observed response shape:

```json
[
  { "id": 413 }
]
```

The response is an array. Empty search results are a successful empty array and
are eligible for negative caching. Release IDs are deduplicated in their first
upstream order before the fan-out limit is applied.

## Torrents for a release

Request:

```http
GET anime/torrents/release/{releaseId}?include=hash,size,label,magnet,seeders,leechers,completed_times,updated_at,release.id,release.type.value,release.year,release.name.main,release.alias
```

The OpenAPI schema declares an array, and live validation returned an array even
when the release had one torrent. The current Jackett adapter also accepts a
single JSON object. For compatibility, this service MUST decode both:

```json
[{ "hash": "...", "release": { "id": 413 } }]
```

and:

```json
{ "hash": "...", "release": { "id": 413 } }
```

Supporting the singleton shape does not claim that it is still emitted by the
current upstream. Any other top-level JSON type is a decode error.

An empty array is a successful result and is negative-cacheable. A `404` means
the requested release no longer exists and is a permanent branch failure, not a
retry candidate.

## Latest torrents

Request:

```http
GET anime/torrents?limit=50&include=hash,size,label,magnet,seeders,leechers,completed_times,updated_at,release.id,release.type.value,release.year,release.name.main,release.alias
```

Response shape:

```json
{
  "data": [
    {
      "hash": "...",
      "release": { "id": 10227 }
    }
  ],
  "meta": {
    "pagination": {
      "total": 3039,
      "count": 50,
      "per_page": 50,
      "current_page": 1,
      "total_pages": 61,
      "links": { "next": "..." }
    }
  }
}
```

The upstream enforces `limit <= 50`; `limit=51` returned HTTP `422` with a field
validation error on the validation date. The service always asks for exactly 50
and ignores upstream pagination metadata beyond basic response validation. It
does not fetch page 2 in the first release.

## `include` behavior

OpenAPI describes `include` as either a comma-separated value or repeated query
parameters and supports dot notation for nested fields. This service uses one
comma-separated value in a stable order.

Keep projections next to the decoder that consumes them and test that fixtures
contain every requested field. Adding a projection field requires a concrete
consumer; removing one requires updating fixtures and mapping tests.

## Required field semantics

| JSON path | Type | Service use |
| --- | --- | --- |
| `hash` | string | lowercase BitTorrent v1 infohash, dedupe key and GUID |
| `size` | integer | payload size in bytes, not `.torrent` file size |
| `label` | string | technical release label and title parsing input |
| `magnet` | string | Torznab grab URI |
| `seeders` | integer | active seed count |
| `leechers` | integer | active non-seed peer count |
| `completed_times` | integer | Torznab grabs |
| `updated_at` | RFC 3339 date-time string | Torznab `pubDate` source |
| `release.id` | number/integer-valued | association and diagnostics |
| `release.type.value` | enum string | category and movie/series behavior |
| `release.year` | number/integer-valued | optional Torznab year attribute |
| `release.name.main` | string | title prefix |
| `release.alias` | string | public details URL |

The OpenAPI release model describes `id` and `year` as JSON numbers; live values
are integer-valued. Decode them without accepting fractional values.

`size`, seeders, leechers, and completed counts must be non-negative. Hash must
be exactly 40 hexadecimal characters and is normalized to lowercase. Magnet must
use the `magnet` scheme. Invalid required data causes that torrent to be omitted
and safely logged; fields are not guessed or reconstructed. One upstream
collection logs at most five item-level validation samples plus one aggregate
warning containing the total and omitted counts. Raw item data and validation
values are never logged.

All upstream strings selected for XML output must contain only characters valid
in XML 1.0. An invalid main name, label, alias/details URL, or magnet invalidates
that item rather than the whole feed. XML-invalid data is not repaired or
silently stripped.

`updated_at` is used instead of `created_at` because AniLiberty torrents are
updated as episodes are added and should reappear as fresh RSS entries. Invalid
or missing `updated_at` makes the item invalid; there is no date fallback.

## Release types

The current OpenAPI enum contains exactly:

```text
TV, ONA, WEB, OVA, OAD, MOVIE, DORAMA, SPECIAL
```

Mapping is defined in [../torznab-contract.md](../torznab-contract.md). Unknown
future values are not coerced to `TV`; they are treated as unsupported data so a
schema change is visible.

## Error and retry classification

The upstream client classifies responses before decoding:

| Condition | Retry? | Classification |
| --- | --- | --- |
| `2xx` with valid JSON | no | success |
| `2xx` with invalid/oversized body | no | permanent response error |
| `429` | yes | rate limited |
| `500`, `502`, `503`, `504` | yes | temporary upstream failure |
| other `4xx` | no | permanent upstream request/not-found error |
| other status | no | unexpected upstream response |
| network error/attempt timeout | no | transport failure |

There are at most three attempts total. All attempt starts pass through the same
global interval limiter. `Retry-After` accepts integer seconds and an HTTP date;
negative or already-expired values do not add delay. Malformed values are
ignored in favor of local backoff and are not logged verbatim.

The response body is read through a hard decompressed-byte limit before JSON
decoding. Decoding from that in-memory body continues to check the parent request
context, including before the first value and while checking for a trailing
value. The configured HTTP timeout applies to each attempt, while the parent
request context may cancel sooner.

## Fixture policy

Public API fixtures MUST be minimized to the fields in the stable `include`
sets and stored without cookies, account data, private trackers, or complete
magnet tracker parameters when those parameters are not necessary for a test.
At least these fixtures are required:

- release search with multiple results;
- empty release search;
- release torrents as an array;
- release torrents as a singleton compatibility object;
- latest `{data,meta}` response;
- one release of every mapped type; and
- validation and temporary-error bodies used by client tests.

Each fixture should record its source endpoint shape and capture date in an
adjacent README or concise comment. Ordinary tests never call the live service.

## Revalidation procedure

When the OpenAPI schema or domain policy appears to change:

1. compare paths, relevant schemas, enum values, and `include` semantics;
2. run opt-in smoke requests against the configured default and official mirror;
3. update fixtures only when the observed contract changed;
4. update this document with the new validation date and evidence; and
5. do not add automatic fallback behavior without an explicit product decision.
