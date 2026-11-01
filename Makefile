VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint check

build:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/ganglion ./cmd/ganglion

test:
	go test -race ./...

lint:
	golangci-lint run

check: lint test build
