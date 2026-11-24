# bleak-node container image.
#
# Build: docker build -t bleak-node .
# Run:   docker compose up -d        (serves 0.0.0.0:8989)
#
# Multi-stage build. SQLite is modernc.org/sqlite (pure Go), so the binary
# is static (CGO_ENABLED=0) and the runtime stage needs no libc.

FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache the module graph first; sources change far more often.
# BuildKit cache mounts keep the Go module + build caches across builds,
# so a rebuild after a code change recompiles only changed packages
# (seconds instead of a full ~1-2 min compile on small VPSes).
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Sources. docs/ is required at build time: the server imports the
# pre-generated swagger docs package (github.com/extube/bleak-node/docs).
COPY cmd/ cmd/
COPY internal/ internal/
COPY docs/ docs/

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o /out/bleak-node ./cmd/server

FROM alpine:3.21
RUN apk add --no-cache ca-certificates su-exec \
    && adduser -D -H -u 1000 bleak
COPY --from=build /out/bleak-node /usr/local/bin/bleak-node
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh \
    && mkdir -p /app/data \
    && chown -R bleak:bleak /app

# Config comes from the environment (docker-compose sets it); the app's
# optional .env loading is a no-op here because no .env ships in the image.
ENV BLEAK_NODE_ADDR=0.0.0.0:8989 \
    BLEAK_NODE_DB=/app/data/bleak-node.db

WORKDIR /app
VOLUME ["/app/data"]
EXPOSE 8989

# Entrypoint starts as root (to fix /app/data ownership), then drops to
# the unprivileged bleak user before exec'ing the server.
ENTRYPOINT ["docker-entrypoint.sh"]
