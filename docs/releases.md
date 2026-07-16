# Release workflow

## Purpose and ownership

This document is the source of truth for release versions, validation, container
publication, and final release notes. Product readiness belongs in
[product-spec.md](product-spec.md), runtime and container design belongs in
[architecture.md](architecture.md), and verified user deployment instructions
belong in the public [README](../README.md). This document defines the active
release process and its immutable-publication guarantees.

**Status:** active release process

**Last updated:** 2026-07-17

## Release artifact

The supported deployment artifact is one public OCI image in GitHub Container
Registry (GHCR):

```text
ghcr.io/zenderg/anilibria-torznab:vX.Y.Z
```

The reference points to one multi-platform image index containing
`linux/amd64` and `linux/arm64` variants. Operators use the same image reference
on both platforms; Docker selects the matching variant. The image contains the
statically linked application binary and the runtime files it actually needs,
not source code or a build toolchain.

GitHub's automatically generated source archives are not deployment artifacts.
Separate binary archives are not published for the first release.

## Release contract

- Releases are cut only from `main`.
- Stable release tags use `vX.Y.Z`, for example `v1.2.3`.
- The Git tag is the single source of truth for the application version. The
  release build derives build metadata and the upstream `User-Agent` version
  from that tag instead of maintaining a second version file.
- Pushing a release tag starts the GitHub Actions release workflow.
- The workflow verifies that the tag has the expected form and identifies a
  commit contained in `main`.
- All required CI and first-release acceptance gates must pass before an image
  is published.
- The workflow builds the image once and assigns all release tags to that same
  image index; it does not rebuild `latest` separately.
- The workflow publishes these image tags:
  - `ghcr.io/zenderg/anilibria-torznab:vX.Y.Z`
  - `ghcr.io/zenderg/anilibria-torznab:X.Y.Z`
  - `ghcr.io/zenderg/anilibria-torznab:latest`
- `latest` moves only for stable releases. Deployments should pin `vX.Y.Z`, or
  the release digest when strict immutability is required.
- The workflow creates a draft GitHub Release containing the image reference,
  image-index digest, supported platforms, and a verified Docker Compose
  snippet.
- A workflow rerun must not overwrite release notes that were already edited.
- Final user-facing notes are reviewed before the GitHub Release is published.

All three tags for a release must initially resolve to the same image-index
digest. Version tags are immutable: after an image has been published, do not
move or rebuild that version tag with different contents. A defect found after
publication is fixed in a new patch release.

After the first publication, verify that the GHCR package is public and linked
to this repository so users can pull it without registry authentication.

## Automated release gates

The release workflow must fail before publication unless all of these checks
succeed:

1. the repository is formatted with `gofmt`;
2. `go test ./...` passes;
3. `go vet ./...` passes;
4. the Docker validation/build stages succeed using the same pinned Go toolchain
   and dependency graph as the production build;
5. both target platforms build successfully;
6. the final image runs as a non-root user;
7. the image requires no writable volume; and
8. a container started from the built image becomes healthy through `/healthz`.

Race tests run in normal CI and in the release validation job. The release job
keeps that verification local to the tagged commit instead of assuming that a
different workflow run tested identical contents.

The manual compatibility evidence listed in the
[first-release acceptance criteria](product-spec.md#first-release-acceptance-criteria),
including the real Prowlarr and Sonarr checks, remains a release gate. GitHub
Actions cannot replace those checks merely because the container smoke test
passes.

## Commit messages

Use the conventional prefixes defined in [AGENTS.md](../AGENTS.md): `feat:`,
`fix:`, `docs:`, `chore:`, `refactor:`, `test:`, and `build:`. They make the
change range easier to review, but release notes are written from the actual
diff and user-visible impact rather than generated blindly from commit subjects.

## Cutting a release

1. Confirm that the product acceptance criteria and required manual compatibility
   gates are satisfied on the release commit.
2. Confirm that the release commit is on `main`, `main` is pushed, and required
   CI is green.
3. Review the changes since the previous release:

   ```bash
   git describe --tags --abbrev=0
   git log --oneline <previous-tag>..HEAD
   ```

   For the first release, where no previous tag exists, review the full history
   ending at the intended release commit.
4. Create and push an annotated tag:

   ```bash
   git tag -a vX.Y.Z -m "vX.Y.Z"
   git push origin vX.Y.Z
   ```

5. Wait for the release workflow to validate and publish the image.
6. Verify that the exact versioned GHCR image has the recorded digest, starts
   through the release Compose example, runs as non-root, and reports healthy.
7. Open the draft GitHub Release and write concise user-facing notes covering:
   - important additions and fixes;
   - configuration, deployment, or compatibility changes;
   - breaking changes and required operator actions;
   - the versioned image reference and digest; and
   - rollback notes when the release changes operator-visible behavior.
8. Publish the GitHub Release.

Release notes live in GitHub Releases rather than in a repository changelog.
The draft release may contain an automatically generated comparison as an
editing aid, but it is not the final user-facing text.

## Deployment and rollback contract

The GitHub Release page provides the verified Compose snippet for its exact
version. Operators should deploy the versioned reference, not `latest`, in
long-lived Compose configuration.

Because the service is stateless and has no persistent application volume,
rollback consists of selecting the previous versioned image and recreating the
Compose service. Configuration compatibility or required operator action must
be called out in release notes before publication.

The public README deployment, upgrade, and rollback commands use the same
release Compose file exercised by CI and the manual compatibility gate.
