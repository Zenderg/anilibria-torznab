# Torznab contract

## Purpose and ownership

This document is the source of truth for the service's Torznab HTTP parameters,
capabilities, category filtering, RSS/XML mapping, paging, and protocol errors.
Product scope belongs in [product-spec.md](product-spec.md), request orchestration
in [architecture.md](architecture.md), AniLiberty JSON in
[integrations/aniliberty.md](integrations/aniliberty.md), and title parsing in
[title-normalization.md](title-normalization.md).

**Status:** first-release protocol baseline

**Last updated:** 2026-07-16

The baseline references are the
[Torznab 1.3 draft](https://torznab.github.io/spec-1.3-draft/torznab/Specification-v1.3.html)
and its inherited
[Newznab API rules](https://torznab.github.io/spec-1.3-draft/external/newznab/api.html).
Prowlarr compatibility is the release gate when the draft leaves behavior
ambiguous.

## General HTTP rules

- The API endpoint is `GET /api`.
- Query parameter names are matched case-insensitively.
- Query values are UTF-8 and URL-decoded exactly once.
- Duplicate singleton parameters are an incorrect parameter error; do not pick
  one silently. `apikey` is the exception: missing or multiple values fail
  authentication with code `100` before other validation.
- Successful and protocol-error responses use HTTP 200 and
  `Content-Type: application/xml; charset=utf-8`.
- Non-`GET` methods use HTTP 405. Unknown paths use HTTP 404. These routing
  failures are outside the Torznab protocol body.
- XML output includes an XML declaration and is encoded as UTF-8.
- The API key is accepted only through the `apikey` query parameter in v1.

All requests to `/api`, including `caps`, require a valid key. This is allowed
for private Torznab services and matches the intended Prowlarr configuration.

## Operations

### `caps`

```http
GET /api?t=caps&apikey=<key>
```

No other parameter affects capabilities. The response MUST declare:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<caps>
  <server version="1.3" title="AniLiberty Torznab" />
  <limits max="50" default="50" />
  <searching>
    <search available="yes" supportedParams="q" />
    <tv-search available="yes" supportedParams="q,season,ep" />
  </searching>
  <categories>
    <category id="5000" name="TV">
      <subcat id="5070" name="Anime" />
    </category>
  </categories>
</caps>
```

Attribute order and indentation are not normative. Movie, audio, book, IMDb,
TMDB, TVDB, TVMaze, and other unsupported capabilities MUST NOT be advertised.
Torznab retention, registration, groups, genres, and tags are omitted.

### `search`

```http
GET /api?t=search&apikey=<key>&q=<query>&cat=<ids>&limit=<n>&offset=<n>&extended=1
```

Parameters:

| Parameter | Required | Behavior |
| --- | --- | --- |
| `q` | no | non-blank means text search; missing/blank means latest RSS |
| `cat` | no | comma-separated standard category IDs |
| `limit` | no | non-negative result count, default 50, clamped to maximum 50 |
| `offset` | no | zero-based number to skip, default 0 |
| `extended` | no | `0` or `1`; accepted for client compatibility |

### `tvsearch`

```http
GET /api?t=tvsearch&apikey=<key>&q=<query>&season=<n>&ep=<n>&cat=<ids>&limit=<n>&offset=<n>&extended=1
```

`q` is required after normalization. `season` and `ep` are optional positive
integers and accept the Torznab forms `13`/`S13` and `13`/`E13`, respectively,
case-insensitively. The prefix is removed before numeric validation. When
present, `ep` matches a single parsed episode or any episode inside a parsed
inclusive range. An item without parsed episode data does not match an `ep`
filter. `season` compares to the explicit or defaulted season derived by the
title contract.

Unambiguous season/episode tokens embedded in `q` MUST supply effective filters
for both `search` and `tvsearch` when the corresponding explicit parameters are
absent. Conflicts are error `201`, as defined by the title-normalization
contract.

The first release does not accept absolute episode notation, `2x03`, fractional
episodes, multi-episode query ranges, or `ep=2/12` as query-parameter values.
Those forms produce error `201` rather than being guessed.

## Category filtering

Upstream types map as follows:

| Upstream type | Result category ID | RSS category text |
| --- | --- | --- |
| `DORAMA` | `5000` | `TV` |
| `TV`, `MOVIE`, `OVA`, `ONA`, `SPECIAL`, `WEB`, `OAD` | `5070` | `TV > Anime` |

`cat` follows Torznab parent-category semantics:

- no `cat` returns all results;
- `cat=5000` returns TV and its Anime subcategory, including dorama;
- `cat=5070` returns Anime results and excludes dorama;
- unknown IDs are ignored when a supported ID is also present;
- a list containing only unknown IDs returns an empty successful feed;
- malformed lists return error `201`; and
- an item is emitted only once even when more than one requested category
  includes it.

## Stable ordering and paging

After validation, deduplication, and filters, results are sorted by:

1. AniLiberty `updated_at` descending; then
2. lowercase infohash ascending.

The service calculates `total` before paging, applies `offset`, then takes
`limit` items. An offset equal to or greater than total returns an empty item
list. `limit=0` returns no items while retaining the pre-page `total`. Values
above 50 are clamped to 50 as recommended by Torznab. Negative or non-numeric
limits and offsets return error `201`.

RSS `<channel>` contains:

```xml
<newznab:response offset="0" total="3" />
```

`offset` is the validated requested offset, even when it lies beyond the result
set. `total` is the count after filters and before paging. For the latest flow,
it is limited to the first 50 upstream items by design.

## RSS document

Successful search output uses RSS 2.0 and both namespaces:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"
     xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"
     xmlns:torznab="http://torznab.com/schemas/2015/feed">
  <channel>
    <title>AniLiberty Torznab</title>
    <description>AniLiberty torrent results</description>
    <link>https://aniliberty.top/</link>
    <newznab:response offset="0" total="1" />
    <item>...</item>
  </channel>
</rss>
```

The channel link comes from `ANILIBRIA_SITE_BASE_URL`; it is not the configured
API root.

## Item mapping

Each valid AniLiberty torrent maps to one `<item>`:

| RSS/XML value | Source or rule |
| --- | --- |
| `title` | title-normalization output |
| `guid` | `urn:btih:<lowercase-infohash>`, `isPermaLink="false"` |
| `link` | upstream magnet URI |
| `comments` | `<site-root>/anime/releases/release/<url-escaped-alias>` |
| `pubDate` | `updated_at` formatted as RFC 1123 with numeric zone |
| `category` | text from the mapping table |
| `enclosure@url` | upstream magnet URI |
| `enclosure@length` | torrent payload size in bytes |
| `enclosure@type` | `application/x-bittorrent` |

The same magnet is also emitted as `torznab:attr name="magneturl"` for explicit
Torznab compatibility. XML serialization MUST escape it; consumers recover the
original URI after parsing.

Every item emits this stable attribute set:

| Attribute | Value |
| --- | --- |
| `category` | `5000` or `5070` |
| `size` | bytes |
| `seeders` | upstream `seeders` |
| `leechers` | upstream `leechers` |
| `peers` | checked sum of seeders and leechers |
| `grabs` | upstream `completed_times` |
| `infohash` | lowercase 40-character hash |
| `magneturl` | upstream magnet URI |
| `downloadvolumefactor` | `0` |
| `uploadvolumefactor` | `1` |
| `year` | release year, only when present and integer-valued |

The service returns the stable attribute set regardless of `extended` because
Prowlarr requires the torrent statistics and the set is inexpensive. `extended`
is validated but does not change output in v1. The `attrs` parameter is not
implemented or advertised; if present it is ignored as an unsupported optional
hint rather than used to suppress required attributes.

Integer addition for `peers` must be checked for overflow even though realistic
upstream values are small.

## Date format

RSS requires RFC 822/1123-style dates. Parse upstream RFC 3339 into a time value,
convert to UTC, and render:

```text
Thu, 16 Jul 2026 19:33:58 +0000
```

Do not emit the literal zone abbreviation `UTC` and do not use current time as a
fallback.

## Title metadata and filters

Title rendering and query cleanup are defined in
[title-normalization.md](title-normalization.md). The parsed representation used
for filtering must be the same representation used to render `SxxEyy`; do not
run a second, divergent parser for filters.

Movies with no parsed episode metadata are valid generic-search results but do
not match an episode-filtered `tvsearch`.

## XML errors

Protocol errors use the inherited Newznab form and HTTP 200:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<error code="100" description="Incorrect user credentials" />
```

The first release uses these safe descriptions and templates:

| Code | Description | Condition |
| --- | --- | --- |
| `100` | `Incorrect user credentials` | missing, duplicate, or incorrect API key |
| `200` | `Missing parameter: <name>` | missing required parameter, such as canonical `t` or `q` |
| `201` | `Incorrect parameter: <name>` | duplicate, malformed, conflicting, or out-of-range canonical parameter |
| `202` | `No such function` | unknown function name |
| `203` | `Function not available` | known Torznab/Newznab function not implemented by this service |
| `900` | `Upstream request failed` | upstream search/latest failed, or every fan-out branch failed |

Descriptions are fixed safe strings. They MUST NOT contain the supplied key,
query, upstream URL, raw upstream body, or internal error text.

Examples:

- `/api?apikey=...` -> `200 Missing parameter: t`
- `/api?t=nope&apikey=...` -> `202 No such function`
- `/api?t=movie&apikey=...` -> `203 Function not available`
- `/api?t=search&limit=-1&apikey=...` -> `201 Incorrect parameter: limit`

Authentication is evaluated before operation-specific validation so an
unauthenticated caller cannot use errors to inspect capabilities or parameters.

## Empty results versus failures

An empty successful feed is not an error. It is returned when:

- AniLiberty successfully finds no matching releases;
- releases successfully contain no torrents;
- filters remove every valid torrent;
- `offset` lies beyond total; or
- an all-unknown but syntactically valid category filter is supplied.

Error `900` is reserved for inability to complete the necessary upstream work,
not for "no matches."

## Prowlarr compatibility checklist

Before the first release, verify against a real Generic Torznab configuration:

- URL points to the service origin, without `/api` duplicated;
- API Path is `/api`;
- API Key is sent for capability and search tests;
- Prowlarr recognizes categories `5000` and `5070`;
- RSS, automatic, and interactive searches are available as intended;
- a magnet result reaches the configured torrent client; and
- enhanced indexer logging parses the response without XML namespace warnings.

After those checks pass, add the exact verified UI instructions and curl
examples to the public README.
