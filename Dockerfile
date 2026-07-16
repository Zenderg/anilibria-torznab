# syntax=docker/dockerfile:1.18@sha256:dabfc0969b935b2080555ace70ee69a5261af8a8f1b4df97b9e7fbcf6722eddf

FROM --platform=$BUILDPLATFORM golang:1.26.5-alpine3.24@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=1970-01-01T00:00:00Z
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN test -z "$(gofmt -l .)" \
    && go test ./... \
    && go vet ./... \
    && CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
      -o /out/anilibria-torznab ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot@sha256:aef9602f8710ec12bde19d593fed1f76c708531bb7aba205110f1029786ead7b

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=1970-01-01T00:00:00Z

LABEL org.opencontainers.image.source="https://github.com/Zenderg/anilibria-torznab" \
      org.opencontainers.image.description="Torznab adapter for AniLiberty" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}"

COPY --from=build /out/anilibria-torznab /anilibria-torznab

EXPOSE 8080
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD ["/anilibria-torznab", "healthcheck"]
ENTRYPOINT ["/anilibria-torznab"]
