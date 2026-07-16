# Development and validation

## Purpose and ownership

This document is the source of truth for contributor setup, local quality
commands, Docker Compose development startup, and opt-in live validation.
Product behavior belongs in [product-spec.md](product-spec.md), runtime design in
[architecture.md](architecture.md), compatibility evidence in
[compatibility.md](compatibility.md), and publication in
[releases.md](releases.md). Temporary investigation notes do not belong here.

**Status:** active first-release workflow

**Last updated:** 2026-07-17

## Requirements

- Go `1.26.5`, as pinned by `go.mod` and the Docker build image;
- Docker Engine with the Compose v2 plugin; and
- network access only for container pulls and explicitly selected live tests.

The implementation uses only the Go standard library, so there is no separate
application dependency-install step.

## Local quality checks

Local Go commands are supported for tests and static checks:

```bash
test -z "$(gofmt -l .)"
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

Ordinary tests are hermetic. HTTP integration tests use loopback
`httptest.Server` instances and never call AniLiberty.

## Start the application

Application startup is supported through Docker Compose rather than a local
`go run` process:

```bash
API_KEY=development-only-key docker compose up -d --build --wait
curl --fail http://127.0.0.1:8080/healthz
API_KEY=development-only-key docker compose down
```

Use `docker compose logs anilibria-torznab` for structured process logs. Logs
must never be copied into an issue without first checking that surrounding
reverse-proxy or client logs have not recorded query-string API keys.

## Opt-in upstream smoke test

The live test validates the minimum response shapes against the configured
AniLiberty API. It is excluded from ordinary test runs and must be enabled
explicitly:

```bash
ANILIBRIA_LIVE_SMOKE=1 go test -run TestLiveAniLibertySmoke -v ./internal/anilibria
```

Set `ANILIBRIA_LIVE_BASE_URL` to test an operator-selected API root. Running the
test once against the default and once against the official mirror is useful
before a stable release. A live failure is evidence to investigate; it must not
lead to an automatic domain fallback.

## Compatibility and release validation

The repeatable real-client scenarios and currently verified versions are in
[compatibility.md](compatibility.md). Release automation and manual gates are in
[releases.md](releases.md). Use temporary client configurations without real
credentials or download paths, keep torrent downloads paused, and remove the
test torrent after verifying the handoff.
