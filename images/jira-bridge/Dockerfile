# jira-bridge: standalone beads→JIRA sync bridge.
# Multi-stage build: Go builder → distroless runtime.
#
# Build:
#   docker build -t gasboat/jira-bridge:latest -f images/jira-bridge/Dockerfile \
#     --build-arg VERSION=$(git describe --tags --always) .

FROM golang:1.25-bookworm AS builder

ARG VERSION=dev
ARG COMMIT=unknown

WORKDIR /build
COPY controller/ ./

RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /jira-bridge ./cmd/jira-bridge/

# ── Runtime ─────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /jira-bridge /jira-bridge

USER nonroot:nonroot
EXPOSE 8091

ENTRYPOINT ["/jira-bridge"]
