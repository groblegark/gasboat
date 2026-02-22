ARG GO_VERSION=1.25
FROM golang:${GO_VERSION} AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /controller ./cmd/controller/

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /controller /controller
USER nonroot:nonroot
ENTRYPOINT ["/controller"]
