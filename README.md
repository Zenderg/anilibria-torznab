# anilibria-torznab

A small, stateless AniLiberty-to-Torznab bridge for using AniLiberty releases as a
Generic Torznab indexer in Prowlarr.

This is an unofficial community project and is not affiliated with AniLiberty,
Prowlarr, Sonarr, or their maintainers.

> [!IMPORTANT]
> This repository is in the design phase. There is no runnable service or
> published container image yet. The documents describe the implementation
> contract for the first release, not already-shipped functionality.

## Why this exists

AniLiberty's API separates release search from torrent lookup. A search returns
release IDs, and each release needs another request to obtain its torrent
variants. Generic Cardigann definitions cannot perform that dynamic fan-out.

This service will bridge the gap:

1. Prowlarr sends a Torznab `search`, `tvsearch`, or RSS request.
2. The service searches AniLiberty releases or requests the latest torrents.
3. It fetches torrent variants with bounded concurrency and upstream rate
   limiting.
4. It returns a Torznab RSS feed with one item per torrent and a magnet link.

## Planned first release

- Torznab `caps`, `search`, and `tvsearch` endpoints
- RSS/latest results when `search` has no query
- deterministic Sonarr-friendly titles
- magnet links, infohashes, sizes, dates, peer statistics, and TV categories
- bounded in-memory caching and duplicate-request coalescing
- partial results when one release lookup fails
- API-key authentication with a public `/healthz` endpoint
- structured logs, graceful shutdown, and conservative upstream protection
- a minimal Go binary and a non-root multi-stage container image

The service will be stateless and will not require a database or persistent
volume. Torrent-file proxying is intentionally outside the first-release scope;
magnet links are the download mechanism.

## Domain policy

AniLiberty is the current project name and `aniliberty.top` is its primary site.
The API remains separately configurable:

- default API: `https://anilibria.top/api/v1/`
- project site and release links: `https://aniliberty.top/`
- official API mirror: `https://api.anilibria.app/api/v1/`

The mirror is an operator-selected alternative, not an automatic fallback. See
the [AniLiberty integration contract](docs/integrations/aniliberty.md) for the
evidence and compatibility notes behind this choice.

## Documentation

- [Product specification](docs/product-spec.md) — goals, scope, configuration,
  and acceptance criteria
- [Architecture](docs/architecture.md) — runtime components, request flows,
  caching, and failure handling
- [AniLiberty integration](docs/integrations/aniliberty.md) — verified upstream
  endpoints and response contracts
- [Torznab contract](docs/torznab-contract.md) — capabilities, query semantics,
  XML mapping, and errors
- [Title normalization](docs/title-normalization.md) — season and episode
  parsing rules

User-facing deployment and Prowlarr setup instructions will be added here once
the container and HTTP API exist and have been verified together. Until then,
the specifications above are the source of truth for implementation work.

## Upstream references

- [AniLiberty API V1 documentation](https://anilibria.top/api/docs/v1)
- [Torznab specification](https://torznab.github.io/spec-1.3-draft/torznab/Specification-v1.3.html)
- [Prowlarr Generic Torznab setup](https://wiki.servarr.com/en/prowlarr/quick-start-guide)

## License

[MIT](LICENSE)
