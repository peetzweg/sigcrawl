# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.26

FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X github.com/peetzweg/sigcrawl/internal/cli.version=${VERSION}" \
    -o /out/sigcrawl ./cmd/sigcrawl

# Build a sigtop binary inside the image so the container is self-contained.
FROM golang:${GO_VERSION}-bookworm AS sigtop-build
RUN apt-get update \
 && apt-get install -y --no-install-recommends gcc libsecret-1-dev pkg-config ca-certificates git \
 && rm -rf /var/lib/apt/lists/*
RUN go install github.com/tbvdm/sigtop@master

FROM debian:bookworm-slim AS runtime
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git libsecret-1-0 \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/sigcrawl /usr/local/bin/sigcrawl
COPY --from=sigtop-build /go/bin/sigtop /usr/local/bin/sigtop
ENV HOME=/data
WORKDIR /data
ENTRYPOINT ["/usr/local/bin/sigcrawl"]
