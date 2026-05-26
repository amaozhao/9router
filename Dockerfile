# syntax=docker/dockerfile:1.7
# Multi-stage build that produces ONE image containing all three Go binaries
# (admin, router, worker) + the static admin-ui assets. docker-compose picks
# which binary to run per service via `command:`.

FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
# CN-friendly module mirror; falls back to upstream on miss.
ENV GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go build -trimpath -ldflags="-s -w" -o /out/admin   ./cmd/admin && \
    go build -trimpath -ldflags="-s -w" -o /out/router  ./cmd/router && \
    go build -trimpath -ldflags="-s -w" -o /out/worker  ./cmd/worker && \
    go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S app && adduser -S -G app app
WORKDIR /app
COPY --from=build /out/admin /out/router /out/worker /out/migrate /app/
COPY --chown=app:app admin-ui /app/admin-ui
COPY --chown=app:app migrations /app/migrations
ENV ADMIN_UI_DIR=/app/admin-ui
USER app
EXPOSE 30100 30200
# No ENTRYPOINT — docker-compose.prod.yml picks the binary per service via
# `command: ["/app/admin"]` etc. Default to router for convenience.
CMD ["/app/router"]
