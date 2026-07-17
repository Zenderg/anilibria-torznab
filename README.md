# anilibria-torznab

A small, stateless AniLiberty-to-Torznab bridge for using AniLiberty releases as
a Generic Torznab indexer in Prowlarr and Sonarr.

This is an unofficial community project and is not affiliated with AniLiberty,
Prowlarr, Sonarr, or their maintainers.

The current stable release is `v1.0.1`. Its deployment artifact is the public
multi-platform image:

```text
ghcr.io/zenderg/anilibria-torznab:v1.0.1
```

## What it does

AniLiberty's API separates release search from torrent lookup. A search returns
release IDs, and each release needs another request to obtain its torrent
variants. This service performs that bounded fan-out and exposes the result as
Torznab:

1. Prowlarr sends a `caps`, `search`, `tvsearch`, or RSS request.
2. The service searches AniLiberty releases or requests the latest torrents.
3. It fetches torrent variants with shared rate limiting, bounded concurrency,
   caching, and duplicate-request coalescing.
4. It returns one Torznab RSS item per torrent with a stable GUID and magnet
   link.

The service has no database or persistent volume. It does not proxy `.torrent`
files or participate in BitTorrent; magnet links are the download mechanism.

## Deploy with Docker Compose

Requirements: Docker Engine with the Compose v2 plugin and an unused local port
(the default is `8080`).

```bash
git clone --branch v1.0.1 --depth 1 https://github.com/Zenderg/anilibria-torznab.git
cd anilibria-torznab

API_KEY="$(openssl rand -hex 32)"
umask 077
printf 'API_KEY=%s\nPORT=8080\nIMAGE=ghcr.io/zenderg/anilibria-torznab:v1.0.1\n' "$API_KEY" > .env

docker compose -f compose.release.yaml pull
docker compose -f compose.release.yaml up -d --wait
```

The release Compose file runs the fixed version as UID/GID `65532`, with a
read-only root filesystem, all Linux capabilities dropped, and no persistent
storage. Check the local health endpoint and Torznab capabilities:

```bash
curl --fail http://127.0.0.1:8080/healthz
curl --get http://127.0.0.1:8080/api \
  --data-urlencode 't=caps' \
  --data-urlencode "apikey=${API_KEY}"
```

Keep `.env` private. Every `/api` operation requires the exact API key;
`/healthz` is intentionally unauthenticated and performs no upstream request.
The service itself serves HTTP, so expose it only on a trusted network or behind
an HTTPS reverse proxy.

## Configure Prowlarr

In **Indexers → Add Indexer → Generic Torznab**, use:

| Field | Value |
| --- | --- |
| Name | `AniLiberty Torznab` |
| URL | the service origin reachable from Prowlarr |
| API Path | `/api` |
| API Key | the value from `.env` |
| Prefer Magnet URL | enabled |

When both applications share a Docker network, the URL is normally
`http://anilibria-torznab:8080`; do not use `127.0.0.1` from inside the Prowlarr
container. Test and save the indexer. Prowlarr should discover TV categories
`5000` and `5070` and the `q`, `season`, and `ep` TV-search parameters.

For Sonarr, sync the indexer from Prowlarr as usual. Configure anime shows with
series type **Anime** so Sonarr uses absolute episode numbers. The verified
compatibility matrix and end-to-end handoff procedure are in
[docs/compatibility.md](docs/compatibility.md).

## Search examples

```bash
# Text search
curl --get http://127.0.0.1:8080/api \
  --data-urlencode 't=search' \
  --data-urlencode 'q=Naruto' \
  --data-urlencode 'limit=10' \
  --data-urlencode "apikey=${API_KEY}"

# Sonarr-style absolute episode search
curl --get http://127.0.0.1:8080/api \
  --data-urlencode 't=tvsearch' \
  --data-urlencode 'q=Naruto Shippuden' \
  --data-urlencode 'season=1' \
  --data-urlencode 'ep=370' \
  --data-urlencode "apikey=${API_KEY}"

# Latest upstream window (at most 50 torrents)
curl --get http://127.0.0.1:8080/api \
  --data-urlencode 't=search' \
  --data-urlencode "apikey=${API_KEY}"
```

## Configuration

Configuration is read once from environment variables at startup. Invalid
configuration stops the process before it serves requests.

| Variable | Default | Meaning |
| --- | --- | --- |
| `API_KEY` | required | shared key for every `/api` operation |
| `LISTEN_ADDR` | `:8080` | HTTP listen address inside the container |
| `ANILIBRIA_API_BASE_URL` | `https://anilibria.top/api/v1/` | selected AniLiberty API root |
| `ANILIBRIA_SITE_BASE_URL` | `https://aniliberty.top/` | public release/details root |
| `REQUEST_TIMEOUT` | `90s` | overall deadline for one Torznab request |
| `HTTP_TIMEOUT` | `15s` | timeout for one upstream attempt |
| `REQUEST_INTERVAL` | `2.1s` | minimum spacing between upstream attempt starts |
| `MAX_CONCURRENCY` | `4` | process-wide simultaneous upstream attempts |
| `MAX_RELEASES_PER_SEARCH` | `10` | maximum release fan-out per search |
| `MAX_RESPONSE_BYTES` | `8MiB` | decompressed upstream response limit |
| `SEARCH_CACHE_TTL` | `10m` | release-search cache lifetime |
| `TORRENTS_CACHE_TTL` | `15m` | per-release torrent cache lifetime |
| `LATEST_CACHE_TTL` | `5m` | latest-window cache lifetime |
| `NEGATIVE_CACHE_TTL` | `1m` | successful empty-result cache lifetime |
| `CACHE_MAX_ENTRIES` | `1024` | maximum entries in each logical cache |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |

Durations use Go duration syntax. Byte sizes accept raw bytes or `KiB`, `MiB`,
and `GiB`. The API base URL can be set to the operator-selected official mirror
`https://api.anilibria.app/api/v1/`; the service never switches domains
automatically. Exact validation bounds are in the
[product specification](docs/product-spec.md#configuration).

Both Compose files forward any defined optional variables from the shell or
`.env`; omitted variables remain absent so the application keeps its defaults.
`PORT` controls the published host port. If `LISTEN_ADDR` uses a container port
other than `8080`, set the Compose-only `CONTAINER_PORT` to the same port (for
example, `LISTEN_ADDR=:9090` and `CONTAINER_PORT=9090`). The listen address must
remain reachable from the container network rather than binding only to
`127.0.0.1`, and its numeric TCP port must be between `1` and `65535`.

## Upgrade and rollback

Edit only the immutable `IMAGE` tag in `.env`, then recreate the service:

```bash
docker compose -f compose.release.yaml pull
docker compose -f compose.release.yaml up -d --wait --remove-orphans
```

To roll back, set `IMAGE` to the previous released `vX.Y.Z` tag and run the same
commands. The service has no persistent state or data migration. Prefer a
versioned tag over `latest` for long-lived deployments.

## Development

Development and validation commands are documented in
[docs/development.md](docs/development.md). Application startup is supported
through Docker Compose; local Go commands are intended for tests and static
checks.

## Documentation

- [Product specification](docs/product-spec.md) — goals, scope, configuration,
  and acceptance criteria
- [Architecture](docs/architecture.md) — runtime components, request flows,
  caching, and failure handling
- [AniLiberty integration](docs/integrations/aniliberty.md) — verified upstream
  endpoints and response contracts
- [Torznab contract](docs/torznab-contract.md) — capabilities, query semantics,
  XML mapping, and errors
- [Title normalization](docs/title-normalization.md) — query, season, episode,
  and Sonarr-compatible title rules
- [Compatibility](docs/compatibility.md) — verified Prowlarr, Sonarr, and
  torrent-client matrix
- [Release workflow](docs/releases.md) — version tags, validation gates, and
  image publication

## Upstream references

- [AniLiberty API V1 documentation](https://anilibria.top/api/docs/v1)
- [Torznab specification](https://torznab.github.io/spec-1.3-draft/torznab/Specification-v1.3.html)
- [Prowlarr Generic Torznab setup](https://wiki.servarr.com/en/prowlarr/quick-start-guide)

## License

[MIT](LICENSE)
