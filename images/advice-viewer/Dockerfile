# advice-viewer: web UI for viewing and managing advice beads.
# Multi-stage build: Go builder -> distroless runtime.
#
# Build:
#   docker build -t gasboat/advice-viewer:latest -f images/advice-viewer/Dockerfile .

FROM golang:1.25-bookworm AS builder

ARG VERSION=dev
ARG COMMIT=unknown

WORKDIR /build
COPY controller/ ./

RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /advice-viewer ./cmd/advice-viewer/

# ── Runtime ─────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /advice-viewer /advice-viewer

USER nonroot:nonroot
EXPOSE 8091

ENTRYPOINT ["/advice-viewer"]
