# AniLiberty API fixtures

## Purpose

This directory is the source of truth for minimized, hermetic AniLiberty V1
response fixtures used by `internal/anilibria` tests. It records endpoint
shapes and sanitization decisions; production behavior and upstream policy
belong in `docs/integrations/aniliberty.md`.

The fixtures were captured or derived from the public API shapes verified on
**2026-07-16**, then reduced to fields consumed by the stable `include`
projections. Values are test-stable examples rather than complete upstream
snapshots.

| File | Public source shape | Purpose |
| --- | --- | --- |
| `search_multiple.json` | `GET app/search/releases?query=Naruto&include=id` | ordered multiple release IDs |
| `search_empty.json` | `GET app/search/releases?query=<no-match>&include=id` | successful empty search |
| `torrents_array.json` | `GET anime/torrents/release/413?include=...` | declared array response and uppercase hash normalization |
| `torrents_singleton.json` | compatibility shape for `GET anime/torrents/release/{id}?include=...` | legacy singleton-object decoding |
| `latest.json` | `GET anime/torrents?limit=50&include=...` | `{data,meta}` envelope |
| `torrents_release_types.json` | release-torrent item shape | one valid item for every mapped V1 release type |
| `validation_error.json` | HTTP 422 validation response shape | permanent validation-error body |
| `temporary_error.json` | HTTP 503 response shape | temporary-error body |

All torrent objects contain exactly the requested torrent/release fields. Magnet
URIs retain only the public `xt` infohash parameter: all tracker (`tr`) parameters
and any unrelated query parameters were removed. The fixtures contain no
cookies, authorization values, account data, or private tracker addresses.

## Opt-in live revalidation

`TestLiveAniLibertySmoke` does not run during ordinary tests. Set
`ANILIBRIA_LIVE_SMOKE=1` to enable it. The primary root can be overridden with
`ANILIBRIA_LIVE_DEFAULT_BASE_URL` (or the normal `ANILIBRIA_API_BASE_URL`), and
the mirror root with `ANILIBRIA_LIVE_MIRROR_BASE_URL`. The smoke test performs
read-only search, release-torrent, and latest requests against both roots.
