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
COPY go.mod go.sum ./
RUN go mod download

# Sources. docs/ is required at build time: the server imports the
# pre-generated swagger docs package (github.com/extube/bleak-node/docs).
COPY cmd/ cmd/
COPY internal/ internal/
COPY docs/ docs/

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o /out/bleak-node ./cmd/server

FROM alpine:3.21
RUN apk add --no-cache ca-certificates \
    && adduser -D -H -u 1000 bleak
COPY --from=build /out/bleak-node /usr/local/bin/bleak-node

# Config comes from the environment (docker-compose sets it); the app's
# optional .env loading is a no-op here because no .env ships in the image.
ENV BLEAK_NODE_ADDR=0.0.0.0:8989 \
    BLEAK_NODE_DB=/app/data/bleak-node.db

WORKDIR /app
VOLUME ["/app/data"]
EXPOSE 8989

USER bleak
ENTRYPOINT ["bleak-node"]
