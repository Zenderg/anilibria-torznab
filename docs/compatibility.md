# Compatibility validation

## Purpose and ownership

This document is the source of truth for the real-client compatibility matrix,
the repeatable Prowlarr/Sonarr/torrent-client validation procedure, and parser
invariants discovered during that validation. Product requirements belong in
[product-spec.md](product-spec.md), wire behavior in
[torznab-contract.md](torznab-contract.md), title rules in
[title-normalization.md](title-normalization.md), and release mechanics in
[releases.md](releases.md). Credentials, raw magnet links, and temporary
environment details must not be recorded here.

**Status:** verified for `v1.0.0`

**Last updated:** 2026-07-17

## Verified matrix

The first-release candidate was validated with clean, temporary client
configurations:

| Component | Version | Verified behavior |
| --- | --- | --- |
| Prowlarr | `2.4.0.5397` | accepted Generic Torznab `caps`; discovered categories `5000` and `5070`; completed text and TV searches without XML or namespace errors |
| Sonarr | `4.0.19.2979` | accepted the Prowlarr-synchronized indexer; matched an Anime-series absolute episode; reported the selected result as download-allowed with no rejection |
| Transmission | `4.1.3-r0-ls354` | received the selected magnet through Sonarr; with `addPaused=true`, reported stopped status, `0%`, and zero transfer rates |

The test search was `Naruto`; it returned nine separate torrent variants. The
Sonarr regression used Naruto Shippuden absolute episode `370`. Sonarr parsed the
selected result as `Naruto Shippuden`, recognized `HDTV-720p`, and handed it to
Transmission. The paused torrent was removed immediately after the zero-transfer
state was confirmed.

The candidate container was also verified healthy as UID/GID `65532`, with a
read-only root filesystem, all capabilities dropped, and no writable volume.

## Title parser invariant

Sonarr identifies the series from the text that precedes the first season and
episode token. Consequently, the normalized result title must begin with:

```text
<semantic label title> SxxEyy[-Eyy]
```

Putting `release.name.main` or the full technical label before that token caused
the real Sonarr parser to report an unknown series. The exact regression example
and normative grammar are maintained in
[title-normalization.md](title-normalization.md#normative-examples).

## Repeatable validation procedure

1. Start the candidate only through Docker Compose and confirm `/healthz`.
2. In a clean Prowlarr configuration, add **Generic Torznab** with the service
   origin, API path `/api`, the configured API key, and **Prefer Magnet URL**.
3. Test the indexer, confirm categories `5000` and `5070`, then search for
   `Naruto` and confirm multiple distinct variants.
4. Configure a clean Sonarr instance and sync the Prowlarr indexer. Add Naruto
   Shippuden as series type **Anime** without enabling monitoring or an automatic
   search.
5. Configure a disposable Transmission instance with `addPaused=true` and no
   existing torrents.
6. Run Sonarr's interactive search for absolute episode `370`. Confirm the
   selected result is download-allowed, has no rejection, and is recognized as
   the expected series and quality.
7. Grab only that result. Confirm Transmission reports stopped status, zero
   completion, and zero transfer rates; then remove the torrent without deleting
   unrelated data.
8. Inspect Prowlarr, Sonarr, and service logs for parser, namespace, transport,
   or credential errors. Do not retain raw API keys, complete request URLs, or
   magnet URIs as evidence.

This procedure verifies metadata handoff only. It intentionally does not
download copyrighted content.

## Upstream and fan-out evidence

The opt-in live smoke test passed against both the default AniLiberty API root
and the official mirror on 2026-07-16. The live torrent endpoint returned an
array during this validation. A singleton-object fixture remains in the
hermetic suite because compatible indexers have historically exposed that
shape, but this document does not claim a current live singleton observation.

The ten-release protection path is covered by an integration test that connects
the real AniLiberty client implementation to the orchestration service. It
proves one shared attempt-start interval, bounded server-observed concurrency,
deterministic ten-result output, and cancellation before all branches start when
the parent deadline is exhausted.
