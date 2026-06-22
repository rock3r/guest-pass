# Stage 1: build frontend assets and Go binary.
FROM golang:1.26-bookworm AS builder
WORKDIR /build

# Download dependencies first for layer-cache efficiency.
COPY go.mod go.sum ./
RUN go mod download

# Copy the full source and build: frontend (esbuild via go run, no node/npm, D-32)
# then the Go binary (pure-Go SQLite = CGO_ENABLED=0 safe).
COPY . .
RUN go run ./cmd/build
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /guestpass ./cmd/guestpass

# Stage 2: minimal runtime image.
# Alpine (~8 MB) is used over distroless so wget is available for the compose healthcheck.
FROM alpine:3.21
RUN addgroup -S guestpass && adduser -S -G guestpass guestpass && \
    mkdir -p /app/web /app/data && \
    chown -R guestpass:guestpass /app

WORKDIR /app
COPY --from=builder --chown=guestpass:guestpass /guestpass /app/guestpass
# web/dist is served at /static/* by http.Dir("web/dist") relative to WORKDIR (/app).
COPY --from=builder --chown=guestpass:guestpass /build/web/dist /app/web/dist

USER guestpass
EXPOSE 8137
ENTRYPOINT ["/app/guestpass", "serve"]
