# syntax = docker/dockerfile:1
ARG GO_BASE_VERSION=1.24.1
FROM golang:$GO_BASE_VERSION AS controller-sources

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/

# Copy the go source
FROM debian:stable-slim AS runner-base
WORKDIR /
RUN useradd -u 65532 -U -m manager
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/manager"]

FROM controller-sources AS controller-builder
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /workspace/manager ./cmd


FROM runner-base AS production
COPY --from=controller-builder /workspace/manager /usr/local/bin/manager

# Build both controller and webhook in dev mode
FROM controller-sources AS dev-builder
RUN --mount=type=cache,target=/root/.cache/go-build \
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /workspace/manager ./cmd

FROM runner-base AS development
COPY --from=dev-builder /workspace/manager /usr/local/bin/manager