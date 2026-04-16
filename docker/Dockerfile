# ============================================================
# dns-geosite-proxy - Multi-stage Dockerfile
# Target: linux/arm/v7 (RouterOS 7.x container)
#
# Build stages:
#   builder  - cross-compiles Go binary for target platform (runs on host arch)
#   runtime  - minimal target-platform Alpine image with binary + crond
#
# Build: make docker-build
# Save:  make docker-save
# ============================================================

# Stage 1: Builder
# Runs on build machine (x86/amd64). Go cross-compilation
# does NOT require QEMU - native toolchain handles it.
FROM --platform=${BUILDPLATFORM:-linux/amd64} golang:1.23-alpine AS builder

ARG TARGETOS=linux
ARG TARGETARCH=arm
ARG TARGETVARIANT=v7

WORKDIR /build

# Copy go.mod/go.sum first for layer caching
COPY src/go.mod src/go.sum ./
RUN go mod download

# Copy source and build
COPY src/ .
RUN set -eux; \
    if [ "${TARGETARCH}" = "arm" ]; then \
      GOARM="${TARGETVARIANT#v}"; \
      export GOARM="${GOARM:-7}"; \
    fi; \
    CGO_ENABLED=0 \
    GOOS="${TARGETOS}" \
    GOARCH="${TARGETARCH}" \
    go build \
      -ldflags="-s -w -extldflags=-static" \
      -o dns-proxy \
      .

# Stage 2: Runtime
# Target platform image. Only curl + crond added on top of Alpine.
FROM --platform=${TARGETPLATFORM:-linux/arm/v7} alpine:3.19

ARG BUILD_DATE
ARG VERSION=dev

LABEL maintainer="dns-geosite-proxy" \
      version="${VERSION}" \
      build-date="${BUILD_DATE}" \
      description="DNS proxy with geosite routing and MikroTik address-list push"

# curl: dlc.dat updates | tzdata: correct cron times | ca-certificates: DoH/TLS
RUN apk add --no-cache \
      ca-certificates \
      curl \
      tzdata

WORKDIR /app

# Copy binary from builder stage
COPY --from=builder /build/dns-proxy .

# Copy helper scripts
COPY docker/entrypoint.sh .
COPY docker/update-dlc.sh .
RUN chmod +x /app/entrypoint.sh /app/update-dlc.sh

# Cron: update dlc.dat every Sunday at 03:00 local time
# Log goes to /var/log/dlc-update.log (stdout accessible via docker logs)
RUN echo "0 3 * * 0 /app/update-dlc.sh >> /var/log/dlc-update.log 2>&1" \
      > /etc/crontabs/root

# Required directories
RUN mkdir -p /etc/dns-proxy /data

# DNS ports: UDP preferred; TCP for large responses (DNSSEC, zone transfers)
EXPOSE 53/udp
EXPOSE 53/tcp

VOLUME ["/data", "/etc/dns-proxy"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD nslookup -type=A cloudflare.com 127.0.0.1 >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/app/entrypoint.sh"]
