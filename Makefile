.PHONY: test build install fmt vet tidy

VERSION ?= dev
LDFLAGS := -X github.com/peetzweg/sigcrawl/internal/cli.version=$(VERSION)

test:
	go test ./...

build:
	go build -ldflags "$(LDFLAGS)" -o bin/sigcrawl ./cmd/sigcrawl

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/sigcrawl

fmt:
	gofmt -s -w .

vet:
	go vet ./...

tidy:
	go mod tidy
