# syntax=docker/dockerfile:1.6

# Build stage runs natively on the build host and cross-compiles to the target
# platform. glebarez/sqlite is pure Go (no CGO), so this is a plain
# cross-compile.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
RUN apk add --no-cache esbuild make curl openssl bash git
WORKDIR /src
COPY . .
# `make generate` (not `go generate` directly) — it also runs
# vendor-frontend-js/fonts, which download and checksum-verify React and the
# fonts into internal/frontend/static/ before go:embed reads the tree. Calling
# `go generate` here would bypass that entirely: the build would stay green
# and the binary would embed an empty vendor directory, so the shipped image
# 404s on every vendored asset with no error anywhere in the pipeline.
RUN make generate
RUN for f in internal/frontend/static/js/app.bundle.js \
             internal/frontend/static/js/vendor/*.js \
             internal/frontend/static/fonts/*.woff2; do \
      [ -s "$f" ] || { echo "vendored asset missing or empty: $f" >&2; exit 1; }; \
    done

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=docker
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/webtools ./cmd/webtools

FROM alpine:3.22
# ca-certificates is not optional here: the whole point of the fetch tool is
# reaching https URLs, and without the trust store every one of them fails
# with an opaque x509 error.
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S webtools && adduser -S webtools -G webtools
WORKDIR /app
COPY --from=builder --chown=webtools:webtools /out/webtools .
# Writable directory for the default sqlite DSN (data/webtools.db).
RUN mkdir -p /app/data && chown -R webtools:webtools /app
USER webtools
EXPOSE 8080
ENTRYPOINT ["./webtools"]
